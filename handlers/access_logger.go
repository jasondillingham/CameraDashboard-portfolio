package handlers

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

const (
	logBufferSize   = 1000
	logFlushSize    = 100
	logFlushTimeout = 5 * time.Second
)

var (
	accessLogChan   chan models.AccessLog
	accessLogOnce   sync.Once
	accessLogWg     sync.WaitGroup
)

// InitAccessLogger starts the background access logger
func InitAccessLogger() {
	accessLogOnce.Do(func() {
		accessLogChan = make(chan models.AccessLog, logBufferSize)
		accessLogWg.Add(1)
		go accessLogWorker()
		log.Printf("Access logger initialized with buffer size %d", logBufferSize)
	})
}

// StopAccessLogger gracefully stops the access logger
func StopAccessLogger() {
	if accessLogChan != nil {
		close(accessLogChan)
		accessLogWg.Wait()
	}
}

// accessLogWorker processes access logs in batches
func accessLogWorker() {
	defer accessLogWg.Done()

	buffer := make([]models.AccessLog, 0, logFlushSize)
	ticker := time.NewTicker(logFlushTimeout)
	defer ticker.Stop()

	flush := func() {
		if len(buffer) == 0 {
			return
		}

		// Copy buffer and reset
		toWrite := make([]models.AccessLog, len(buffer))
		copy(toWrite, buffer)
		buffer = buffer[:0]

		// Write to database
		if err := db.InsertAccessLogBatch(toWrite); err != nil {
			log.Printf("Error writing access logs: %v", err)
		}
	}

	for {
		select {
		case entry, ok := <-accessLogChan:
			if !ok {
				// Channel closed, flush remaining and exit
				flush()
				return
			}
			buffer = append(buffer, entry)
			if len(buffer) >= logFlushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// LogAccess queues an access log entry (non-blocking)
func LogAccess(entry models.AccessLog) {
	if accessLogChan == nil {
		return
	}

	select {
	case accessLogChan <- entry:
	default:
		// Buffer full, drop the entry
		log.Printf("Access log buffer full, dropping entry for %s", entry.Path)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// AccessLoggingMiddleware logs all requests to the access log
func AccessLoggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip logging for certain paths
		path := r.URL.Path
		if shouldSkipLogging(path) {
			next(w, r)
			return
		}

		// Wrap response writer to capture status code
		rw := newResponseWriter(w)

		// Call the handler
		next(rw, r)

		// Log the access
		user := contextGetAuthenticatedUser(r)
		userID := ""
		userName := ""
		if user != nil {
			userID = user.SamAccountName
			userName = user.DisplayName
		}

		entry := models.AccessLog{
			UserID:     userID,
			UserName:   userName,
			Path:       path,
			Method:     r.Method,
			StatusCode: rw.statusCode,
			ClientIP:   extractClientIP(r),
			UserAgent:  truncateString(r.UserAgent(), 500),
			Timestamp:  time.Now(),
		}

		LogAccess(entry)
	}
}

// shouldSkipLogging returns true for paths that should not be logged
func shouldSkipLogging(path string) bool {
	// Skip static assets, partials, and high-frequency endpoints
	skipPrefixes := []string{
		"/static/",
		"/api/version",
		"/api/heartbeat",
	}
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	// Skip partial endpoints (they refresh frequently)
	if strings.Contains(path, "/partial") {
		return true
	}

	return false
}

// extractClientIP gets the client IP from the request
func extractClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for reverse proxy)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	// Handle IPv6 brackets
	ip = strings.TrimPrefix(ip, "[")
	ip = strings.TrimSuffix(ip, "]")

	return ip
}

// truncateString truncates a string to maxLen
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// ProtectedWithLogging wraps a handler with auth, permission checks, and access logging
func ProtectedWithLogging(h http.HandlerFunc) http.HandlerFunc {
	return authenticate(requireAuth(requireDashboardPermission(AccessLoggingMiddleware(h))))
}
