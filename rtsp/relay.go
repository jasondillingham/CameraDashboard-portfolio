package rtsp

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
)

// RelaySession holds the target NVR URL and playback scale for a relay session.
type RelaySession struct {
	TargetURL string
	Scale     float64
	Username  string
	Password  string
}

// Relay is a lightweight RTSP proxy that injects the Scale header into PLAY
// requests. go2rtc connects to the relay as a source, and the relay forwards
// the connection to the real NVR, adding the Scale header for speed control.
// The relay handles NVR digest authentication itself so go2rtc doesn't need credentials.
type Relay struct {
	mu       sync.RWMutex
	sessions map[string]*RelaySession
	listener net.Listener
	port     int
}

// NewRelay creates a new RTSP relay on the given port.
func NewRelay(port int) *Relay {
	return &Relay{
		sessions: make(map[string]*RelaySession),
		port:     port,
	}
}

// Start begins listening for RTSP connections. Blocks until the listener is closed.
func (r *Relay) Start() error {
	var err error
	r.listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", r.port))
	if err != nil {
		return fmt.Errorf("RTSP relay: failed to listen on port %d: %w", r.port, err)
	}
	log.Printf("RTSP relay: listening on 127.0.0.1:%d", r.port)

	for {
		conn, err := r.listener.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed") {
				return nil
			}
			log.Printf("RTSP relay: accept error: %v", err)
			continue
		}
		go r.handleConnection(conn)
	}
}

// RegisterSession stores session metadata for the relay.
// The targetURL should include credentials: rtsp://user:pass@host:port/path
func (r *Relay) RegisterSession(sessionID, targetURL string, scale float64) {
	username, password := extractCredentials(targetURL)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sessionID] = &RelaySession{
		TargetURL: targetURL,
		Scale:     scale,
		Username:  username,
		Password:  password,
	}
}

// UnregisterSession removes a session from the relay.
func (r *Relay) UnregisterSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, sessionID)
}

// GetRelayURL returns the RTSP URL that go2rtc should use as the stream source.
func (r *Relay) GetRelayURL(sessionID string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/relay/%s", r.port, sessionID)
}

// getSession returns a copy of the session metadata.
func (r *Relay) getSession(sessionID string) (*RelaySession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return nil, false
	}
	return &RelaySession{
		TargetURL: s.TargetURL,
		Scale:     s.Scale,
		Username:  s.Username,
		Password:  s.Password,
	}, true
}

// handleConnection manages an RTSP session between go2rtc (frontend) and NVR (backend).
// The relay authenticates to the NVR using digest auth and presents an unauthenticated
// interface to go2rtc. It injects the Scale header on PLAY requests.
func (r *Relay) handleConnection(frontend net.Conn) {
	defer frontend.Close()

	frontReader := bufio.NewReaderSize(frontend, 64*1024)

	// Read the first RTSP request to extract session ID
	firstReqLine, firstHeaders, err := readRTSPMessage(frontReader)
	if err != nil {
		log.Printf("RTSP relay: failed to read first request: %v", err)
		return
	}

	parts := strings.Fields(firstReqLine)
	if len(parts) < 3 {
		log.Printf("RTSP relay: invalid first line: %s", firstReqLine)
		return
	}

	relayURI := parts[1]
	sessionID := extractSessionID(relayURI)
	if sessionID == "" {
		log.Printf("RTSP relay: no session ID in URI: %s", relayURI)
		return
	}

	sess, ok := r.getSession(sessionID)
	if !ok {
		log.Printf("RTSP relay: unknown session %s", sessionID)
		return
	}

	targetHost := extractHost(sess.TargetURL)
	if targetHost == "" {
		log.Printf("RTSP relay: cannot parse host from %s", sess.TargetURL)
		return
	}

	// Connect to NVR
	backend, err := net.Dial("tcp", targetHost)
	if err != nil {
		log.Printf("RTSP relay: failed to connect to NVR %s: %v", targetHost, err)
		return
	}
	defer backend.Close()

	log.Printf("RTSP relay: session %s connected to %s (scale=%.0f)", sessionID, targetHost, sess.Scale)

	backReader := bufio.NewReaderSize(backend, 64*1024)

	// State for digest authentication
	var digestAuth *digestChallenge
	var nvrSessionID string // RTSP Session header from NVR

	// Process RTSP requests from go2rtc, authenticate with NVR, relay responses back
	// The first request has already been read
	method := parts[0]
	reqLine := firstReqLine
	headers := firstHeaders

	for {
		// Rewrite URI from relay to target NVR
		targetURI := rewriteURI(reqLine, relayURI, sess.TargetURL)
		rewrittenLine := strings.Replace(reqLine, parts[1], targetURI, 1)

		// Extract CSeq from frontend request
		cseq := getHeader(headers, "CSeq")

		// Build the request to send to NVR
		isPlay := (method == "PLAY")

		// Send request to NVR (with auth if we have it)
		nvrResp, nvrRespHeaders, nvrBody, err := r.sendToNVR(
			backend, backReader, rewrittenLine, headers, targetURI,
			sess, digestAuth, isPlay, nvrSessionID,
		)
		if err != nil {
			log.Printf("RTSP relay: NVR request failed: %v", err)
			return
		}

		// Handle 401 - parse digest challenge and retry with credentials
		if strings.Contains(nvrResp, "401") {
			challenge := parseWWWAuthenticate(nvrRespHeaders)
			if challenge != nil && sess.Username != "" {
				digestAuth = challenge
				log.Printf("RTSP relay: got 401, retrying %s with digest auth", method)

				// Retry with auth
				nvrResp, nvrRespHeaders, nvrBody, err = r.sendToNVR(
					backend, backReader, rewrittenLine, headers, targetURI,
					sess, digestAuth, isPlay, nvrSessionID,
				)
				if err != nil {
					log.Printf("RTSP relay: NVR retry failed: %v", err)
					return
				}
			}
		}

		// Extract NVR Session header for future requests
		if sid := getHeader(nvrRespHeaders, "Session"); sid != "" {
			// Session may include timeout: "abc123;timeout=60"
			nvrSessionID = strings.Split(sid, ";")[0]
		}

		// Build response back to go2rtc
		// Use the frontend's CSeq, not the NVR's
		respHeaders := replaceCSeq(nvrRespHeaders, cseq)

		// Rewrite SDP control URIs so go2rtc sends SETUP back through relay
		bodyStr := string(nvrBody)
		if len(nvrBody) > 0 {
			bodyStr = rewriteSDPControl(bodyStr, sess.TargetURL, relayURI)
		}

		// Send response to go2rtc
		frontend.Write([]byte(nvrResp))
		frontend.Write([]byte(respHeaders))
		if len(bodyStr) > 0 {
			// Update Content-Length in case SDP rewrite changed size
			frontend.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n", len(bodyStr))))
		}
		frontend.Write([]byte("\r\n"))
		if len(bodyStr) > 0 {
			frontend.Write([]byte(bodyStr))
		}

		// After PLAY response, switch to raw bidirectional copy for RTP data
		if isPlay && strings.Contains(nvrResp, "200") {
			log.Printf("RTSP relay: PLAY accepted, switching to raw relay")
			// Bidirectional copy
			done := make(chan struct{})
			go func() {
				io.Copy(backend, frontReader)
				close(done)
			}()
			io.Copy(frontend, backReader)
			<-done
			return
		}

		// Read next request from go2rtc
		reqLine, headers, err = readRTSPMessage(frontReader)
		if err != nil {
			return
		}
		parts = strings.Fields(reqLine)
		if len(parts) < 3 {
			return
		}
		method = parts[0]
	}
}

// sendToNVR sends an RTSP request to the NVR and reads the response.
func (r *Relay) sendToNVR(
	backend net.Conn, backReader *bufio.Reader,
	reqLine, frontendHeaders, targetURI string,
	sess *RelaySession, auth *digestChallenge,
	isPlay bool, nvrSession string,
) (respLine string, respHeaders string, body []byte, err error) {

	// Build headers for NVR
	var sb strings.Builder
	sb.WriteString(reqLine)

	// Copy relevant headers from frontend, skip Authorization
	for _, line := range strings.Split(frontendHeaders, "\r\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lowerLine := strings.ToLower(trimmed)
		// Skip headers we'll set ourselves
		if strings.HasPrefix(lowerLine, "authorization:") {
			continue
		}
		if strings.HasPrefix(lowerLine, "session:") && nvrSession != "" {
			continue // we'll add our own
		}
		sb.WriteString(line + "\r\n")
	}

	// Add NVR Session header if we have one
	if nvrSession != "" {
		sb.WriteString("Session: " + nvrSession + "\r\n")
	}

	// Add digest auth if available
	if auth != nil && sess.Username != "" {
		method := strings.Fields(reqLine)[0]
		authHeader := computeDigestAuth(auth, sess.Username, sess.Password, method, targetURI)
		sb.WriteString("Authorization: " + authHeader + "\r\n")
	}

	// Inject Scale header on PLAY
	if isPlay && sess.Scale > 1 {
		sb.WriteString(fmt.Sprintf("Scale: %.6f\r\n", sess.Scale))
	}

	sb.WriteString("\r\n")

	_, err = backend.Write([]byte(sb.String()))
	if err != nil {
		return "", "", nil, fmt.Errorf("write to NVR: %w", err)
	}

	// Read response
	respLine, err = readLine(backReader)
	if err != nil {
		return "", "", nil, fmt.Errorf("read NVR response: %w", err)
	}

	respHeaders, err = readHeaderBlock(backReader)
	if err != nil {
		return respLine, "", nil, fmt.Errorf("read NVR headers: %w", err)
	}

	// Read body if Content-Length present
	cl := parseContentLength(respHeaders)
	if cl > 0 {
		body = make([]byte, cl)
		_, err = io.ReadFull(backReader, body)
		if err != nil {
			return respLine, respHeaders, nil, fmt.Errorf("read NVR body: %w", err)
		}
	}

	return respLine, respHeaders, body, nil
}

// readRTSPMessage reads a request line and headers from the reader.
func readRTSPMessage(r *bufio.Reader) (reqLine string, headers string, err error) {
	reqLine, err = readLine(r)
	if err != nil {
		return "", "", err
	}
	headers, err = readHeaderBlock(r)
	return reqLine, headers, err
}

// readLine reads a single \n-terminated line.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

// readHeaderBlock reads RTSP headers until blank line (\r\n).
// Returns all header lines as a single string (each ending in \r\n),
// NOT including the terminating blank line.
func readHeaderBlock(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return sb.String(), err
		}
		if line == "\r\n" || line == "\n" {
			return sb.String(), nil
		}
		sb.WriteString(line)
	}
}

// getHeader extracts a header value by name (case-insensitive).
func getHeader(headers, name string) string {
	lower := strings.ToLower(name) + ":"
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), lower) {
			return strings.TrimSpace(line[strings.Index(line, ":")+1:])
		}
	}
	return ""
}

// replaceCSeq replaces the CSeq header value in the response headers.
func replaceCSeq(headers, cseq string) string {
	if cseq == "" {
		return headers
	}
	var sb strings.Builder
	found := false
	for _, line := range strings.Split(headers, "\r\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "cseq:") {
			sb.WriteString("CSeq: " + cseq + "\r\n")
			found = true
		} else if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			// Skip - we'll add our own if needed
			continue
		} else {
			sb.WriteString(line + "\r\n")
		}
	}
	if !found && cseq != "" {
		sb.WriteString("CSeq: " + cseq + "\r\n")
	}
	return sb.String()
}

// --- Digest Authentication ---

type digestChallenge struct {
	realm  string
	nonce  string
	qop    string
	opaque string
}

// parseWWWAuthenticate parses a Digest WWW-Authenticate header from the response.
func parseWWWAuthenticate(headers string) *digestChallenge {
	for _, line := range strings.Split(headers, "\r\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), "www-authenticate:") {
			continue
		}
		val := strings.TrimSpace(trimmed[strings.Index(trimmed, ":")+1:])
		if !strings.HasPrefix(val, "Digest ") {
			continue
		}
		params := val[7:]
		dc := &digestChallenge{}
		dc.realm = extractParam(params, "realm")
		dc.nonce = extractParam(params, "nonce")
		dc.qop = extractParam(params, "qop")
		dc.opaque = extractParam(params, "opaque")
		return dc
	}
	return nil
}

// extractParam gets a quoted parameter value from a digest header.
func extractParam(params, name string) string {
	key := name + "="
	idx := strings.Index(strings.ToLower(params), strings.ToLower(key))
	if idx < 0 {
		return ""
	}
	rest := params[idx+len(key):]
	if len(rest) > 0 && rest[0] == '"' {
		// Quoted value
		end := strings.Index(rest[1:], "\"")
		if end >= 0 {
			return rest[1 : end+1]
		}
	}
	// Unquoted value
	end := strings.IndexAny(rest, ", \t")
	if end >= 0 {
		return rest[:end]
	}
	return rest
}

// computeDigestAuth computes a Digest Authorization header value.
func computeDigestAuth(dc *digestChallenge, username, password, method, uri string) string {
	ha1 := md5hex(username + ":" + dc.realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)

	var response string
	if dc.qop == "auth" || strings.Contains(dc.qop, "auth") {
		nc := "00000001"
		cnonce := fmt.Sprintf("%08x", rand.Int31())
		response = md5hex(ha1 + ":" + dc.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		result := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s"`,
			username, dc.realm, dc.nonce, uri, nc, cnonce, response)
		if dc.opaque != "" {
			result += fmt.Sprintf(`, opaque="%s"`, dc.opaque)
		}
		return result
	}

	// No QoP
	response = md5hex(ha1 + ":" + dc.nonce + ":" + ha2)
	result := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, dc.realm, dc.nonce, uri, response)
	if dc.opaque != "" {
		result += fmt.Sprintf(`, opaque="%s"`, dc.opaque)
	}
	return result
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

// --- URI and URL Helpers ---

// extractCredentials parses username and password from rtsp://user:pass@host/path
func extractCredentials(rtspURL string) (username, password string) {
	u := strings.TrimPrefix(rtspURL, "rtsp://")
	at := strings.Index(u, "@")
	if at < 0 {
		return "", ""
	}
	userinfo := u[:at]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return userinfo, ""
	}
	return userinfo[:colon], userinfo[colon+1:]
}

// extractSessionID gets the session ID from /relay/{sessionID}
func extractSessionID(uri string) string {
	idx := strings.Index(uri, "/relay/")
	if idx < 0 {
		return ""
	}
	rest := uri[idx+7:]
	if i := strings.IndexAny(rest, "?/ "); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// extractHost gets host:port from an RTSP URL (stripping credentials)
func extractHost(rtspURL string) string {
	u := strings.TrimPrefix(rtspURL, "rtsp://")
	if at := strings.Index(u, "@"); at >= 0 {
		u = u[at+1:]
	}
	if slash := strings.Index(u, "/"); slash >= 0 {
		u = u[:slash]
	}
	if !strings.Contains(u, ":") {
		u += ":554"
	}
	return u
}

// extractPath gets the path+query from an RTSP URL (stripping scheme, userinfo, host)
func extractPath(rtspURL string) string {
	u := strings.TrimPrefix(rtspURL, "rtsp://")
	if at := strings.Index(u, "@"); at >= 0 {
		u = u[at+1:]
	}
	if slash := strings.Index(u, "/"); slash >= 0 {
		return u[slash:]
	}
	return "/"
}

// rewriteURI maps a relay URI to the corresponding NVR URI.
// Input: go2rtc sends URIs like rtsp://127.0.0.1:15554/relay/{id} or
// rtsp://127.0.0.1:15554/relay/{id}/trackID=video etc.
// Output: the corresponding NVR URI path+query.
func rewriteURI(reqLine, relayURI, targetURL string) string {
	// Get the URI from the request line
	reqParts := strings.Fields(reqLine)
	if len(reqParts) < 2 {
		return targetURL
	}
	currentURI := reqParts[1]

	// Get relay base (without query)
	relayBase := relayURI
	if i := strings.Index(relayBase, "?"); i >= 0 {
		relayBase = relayBase[:i]
	}

	// Strip credentials from target URL for URI construction
	targetNoAuth := stripCredentials(targetURL)

	targetBase := targetNoAuth
	if i := strings.Index(targetBase, "?"); i >= 0 {
		targetBase = targetBase[:i]
	}
	targetQuery := ""
	if i := strings.Index(targetNoAuth, "?"); i >= 0 {
		targetQuery = targetNoAuth[i:]
	}

	// Replace relay base with target base
	result := strings.Replace(currentURI, relayBase, targetBase, 1)

	// Add query params for non-track URIs
	if targetQuery != "" && !strings.Contains(result, "starttime=") {
		fields := strings.Fields(result)
		uri := result
		if len(fields) > 0 {
			uri = fields[0]
		}
		if !strings.Contains(uri, "/trackID") {
			result = uri + targetQuery
		}
	}

	return result
}

// stripCredentials removes user:pass@ from an RTSP URL.
func stripCredentials(rtspURL string) string {
	if !strings.HasPrefix(rtspURL, "rtsp://") {
		return rtspURL
	}
	rest := rtspURL[7:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return rtspURL
	}
	return "rtsp://" + rest[at+1:]
}

// parseContentLength extracts Content-Length from headers.
func parseContentLength(headers string) int {
	val := getHeader(headers, "Content-Length")
	if val == "" {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(val))
	return n
}

// rewriteSDPControl rewrites absolute RTSP URIs in SDP a=control: attributes
// so that go2rtc's SETUP requests come back through the relay.
func rewriteSDPControl(sdp, targetURL, relayURI string) string {
	targetNoAuth := stripCredentials(targetURL)
	targetBase := targetNoAuth
	if i := strings.Index(targetBase, "?"); i >= 0 {
		targetBase = targetBase[:i]
	}
	relayBase := relayURI
	if i := strings.Index(relayBase, "?"); i >= 0 {
		relayBase = relayBase[:i]
	}
	return strings.ReplaceAll(sdp, targetBase, relayBase)
}
