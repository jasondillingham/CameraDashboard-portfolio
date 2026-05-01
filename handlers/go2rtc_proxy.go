package handlers

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"cameradashboard/internal/auth"
)

// RegisterGo2RTCProxy registers the go2rtc reverse proxy on the default mux.
// Called BEFORE session middleware routes so WebSocket upgrades work
// (session middleware wraps ResponseWriter and breaks http.Hijacker).
func RegisterGo2RTCProxy() {
	target, err := url.Parse(go2rtcBaseURL)
	if err != nil {
		log.Fatalf("invalid go2rtc URL %q: %v", go2rtcBaseURL, err)
	}

	// Standard reverse proxy for regular HTTP requests (JS files, API calls)
	httpProxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/go2rtc")
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("go2rtc proxy error: %s %s -> %v", r.Method, r.URL.Path, err)
			http.Error(w, "go2rtc unreachable", http.StatusBadGateway)
		},
	}

	http.HandleFunc("/go2rtc/", func(w http.ResponseWriter, r *http.Request) {
		// WebSocket upgrade — pass through for camera streams (no hijack-breaking middleware)
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			proxyWebSocket(w, r, target)
			return
		}

		// Player-facing paths (JS, stream API, WebRTC) pass through for
		// camera functionality on authenticated dashboard pages.
		innerPath := strings.TrimPrefix(r.URL.Path, "/go2rtc")
		if !isGo2RTCManagementPath(innerPath) {
			httpProxy.ServeHTTP(w, r)
			return
		}

		// Management UI and admin API endpoints — require admin auth.
		// We inline sm.LoadAndSave here (safe for HTTP, only breaks Hijacker).
		sm := GetSessionManager()
		sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := sm.GetString(r.Context(), "authenticatedUserID")
			if userID == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			user := &auth.User{
				SamAccountName: userID,
				DisplayName:    sm.GetString(r.Context(), "authenticatedUserName"),
			}
			if !IsAdmin(user) {
				http.Error(w, "Forbidden - Admin access required", http.StatusForbidden)
				return
			}
			httpProxy.ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})

	// Register per-NVR go2rtc proxies for NVRs with local go2rtc servers.
	// These proxy /go2rtc-nvr/{nvrId}/... to the NVR's own go2rtc instance.
	registerPerNVRProxies()
}

// registerPerNVRProxies creates reverse proxy routes for NVRs that have
// their own local go2rtc server (Go2RTCURL set in config).
func registerPerNVRProxies() {
	cameraNVRsMu.RLock()
	defer cameraNVRsMu.RUnlock()

	for _, nvr := range cameraNVRs {
		if nvr.Go2RTCURL == "" {
			continue
		}

		nvrTarget, err := url.Parse(nvr.Go2RTCURL)
		if err != nil {
			log.Printf("Camera Dashboard: invalid go2rtc URL %q for NVR %s: %v", nvr.Go2RTCURL, nvr.ID, err)
			continue
		}

		prefix := "/go2rtc-nvr/" + nvr.ID + "/"
		nvrID := nvr.ID // capture for closure

		nvrProxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = nvrTarget.Scheme
				req.URL.Host = nvrTarget.Host
				req.Host = nvrTarget.Host
				req.URL.Path = strings.TrimPrefix(req.URL.Path, "/go2rtc-nvr/"+nvrID)
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
				// Add basic auth if the remote go2rtc requires it
				if nvrTarget.User != nil {
					pass, _ := nvrTarget.User.Password()
					req.SetBasicAuth(nvrTarget.User.Username(), pass)
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("go2rtc proxy [%s] error: %s %s -> %v", nvrID, r.Method, r.URL.Path, err)
				http.Error(w, "go2rtc unreachable for "+nvrID, http.StatusBadGateway)
			},
		}

		http.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				proxyWebSocket(w, r, nvrTarget)
				return
			}
			nvrProxy.ServeHTTP(w, r)
		})

		log.Printf("Camera Dashboard: Registered per-NVR go2rtc proxy %s -> %s", prefix, sanitizeURL(nvr.Go2RTCURL))
	}
}

// isGo2RTCManagementPath returns true for the go2rtc web UI and management
// endpoints that should be restricted to admins. Player-facing paths (JS files,
// stream API, WebSocket API, WebRTC signaling) are NOT management paths.
func isGo2RTCManagementPath(path string) bool {
	// JS files are needed by the camera player
	if strings.HasSuffix(path, ".js") {
		return false
	}
	// HTML pages are the go2rtc web UI
	if path == "/" || strings.HasSuffix(path, ".html") {
		return true
	}
	// Block management API endpoints; allow player-facing ones
	if strings.HasPrefix(path, "/api/") {
		switch {
		case strings.HasPrefix(path, "/api/ws"):
			return false // WebSocket player
		case strings.HasPrefix(path, "/api/webrtc"):
			return false // WebRTC signaling
		case strings.HasPrefix(path, "/api/frame"):
			return false // JPEG snapshots
		case path == "/api/streams":
			return false // stream health check (read-only GET)
		case strings.HasPrefix(path, "/api/stream."):
			return false // stream.mp4, stream.mjpeg, etc.
		default:
			return true // /api/config, /api/log, /api/restart, etc.
		}
	}
	// Block everything else (CSS, icons, unknown paths)
	return true
}

// proxyWebSocket handles WebSocket connections by hijacking the client connection
// and establishing a raw TCP tunnel to the upstream go2rtc server.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, target *url.URL) {
	// Connect to upstream go2rtc
	upstreamConn, err := net.DialTimeout("tcp", target.Host, 10*time.Second)
	if err != nil {
		log.Printf("go2rtc ws proxy: dial to %s failed for %s: %v", target.Host, r.URL.Path, err)
		http.Error(w, "go2rtc unreachable", http.StatusBadGateway)
		return
	}

	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		log.Printf("go2rtc ws proxy: ResponseWriter does not support Hijack")
		http.Error(w, "websocket proxy not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		log.Printf("go2rtc ws proxy: hijack failed: %v", err)
		return
	}

	// Rewrite the request URL to strip the proxy prefix and forward to upstream.
	// Handles both /go2rtc/... and /go2rtc-nvr/{nvrId}/... prefixes.
	upstreamPath := r.URL.Path
	if strings.HasPrefix(upstreamPath, "/go2rtc-nvr/") {
		// Strip /go2rtc-nvr/{nvrId} prefix
		parts := strings.SplitN(strings.TrimPrefix(upstreamPath, "/go2rtc-nvr/"), "/", 2)
		if len(parts) > 1 {
			upstreamPath = "/" + parts[1]
		} else {
			upstreamPath = "/"
		}
	} else {
		upstreamPath = strings.TrimPrefix(upstreamPath, "/go2rtc")
	}
	if upstreamPath == "" {
		upstreamPath = "/"
	}
	if r.URL.RawQuery != "" {
		upstreamPath += "?" + r.URL.RawQuery
	}

	// Build the raw HTTP upgrade request
	var reqBuf strings.Builder
	reqBuf.WriteString("GET " + upstreamPath + " HTTP/1.1\r\n")
	reqBuf.WriteString("Host: " + target.Host + "\r\n")

	// Forward all headers, rewriting Host and Origin for upstream
	for key, values := range r.Header {
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, v := range values {
			// Rewrite Origin to match upstream so go2rtc doesn't reject it
			if strings.EqualFold(key, "Origin") {
				v = target.Scheme + "://" + target.Host
			}
			reqBuf.WriteString(key + ": " + v + "\r\n")
		}
	}

	// Add basic auth if the target URL contains credentials
	if target.User != nil {
		pass, _ := target.User.Password()
		authReq := &http.Request{Header: http.Header{}}
		authReq.SetBasicAuth(target.User.Username(), pass)
		reqBuf.WriteString("Authorization: " + authReq.Header.Get("Authorization") + "\r\n")
	}

	reqBuf.WriteString("\r\n")

	// Send the upgrade request to upstream
	if _, err := upstreamConn.Write([]byte(reqBuf.String())); err != nil {
		log.Printf("go2rtc ws proxy: write upstream failed: %v", err)
		clientConn.Close()
		upstreamConn.Close()
		return
	}

	// Bidirectional copy — relay data between client and upstream.
	// When one side closes, shut down the other to prevent goroutine leaks.
	done := make(chan struct{}, 2)

	// Upstream → Client
	go func() {
		n, err := io.Copy(clientConn, upstreamConn)
		if err != nil {
			log.Printf("go2rtc ws proxy: upstream→client error after %d bytes: %v", n, err)
		}
		clientConn.Close()
		done <- struct{}{}
	}()

	// Client → Upstream: first flush any buffered data, then copy
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		n, err := clientBuf.Read(buffered)
		if err != nil {
			log.Printf("go2rtc ws proxy: buffered read error: %v", err)
		}
		if n > 0 {
			if _, err := upstreamConn.Write(buffered[:n]); err != nil {
				log.Printf("go2rtc ws proxy: buffered write error: %v", err)
			}
		}
	}

	// Client → Upstream
	go func() {
		n, err := io.Copy(upstreamConn, clientConn)
		if err != nil {
			log.Printf("go2rtc ws proxy: client→upstream error after %d bytes: %v", n, err)
		}
		upstreamConn.Close()
		done <- struct{}{}
	}()

	// Wait for one direction to finish, then close both to unblock the other
	<-done
	clientConn.Close()
	upstreamConn.Close()
	<-done
}
