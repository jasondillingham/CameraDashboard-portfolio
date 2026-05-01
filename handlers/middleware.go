package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cameradashboard/internal/auth"

	"github.com/alexedwards/scs/v2"
)

var (
	sessionManager *scs.SessionManager
	ldapClient     *auth.LDAPClient
	allowedUsers   map[string]bool
	adminUsers     map[string]bool
	kioskManager   *auth.KioskManager

	// lockoutMode blocks all non-admin logins and sessions when true.
	// Toggled via the admin kill switch.
	lockoutMode   bool
	lockoutMu     sync.RWMutex
)

// IsLockoutMode returns whether lockout mode is active.
func IsLockoutMode() bool {
	lockoutMu.RLock()
	defer lockoutMu.RUnlock()
	return lockoutMode
}

// SetLockoutMode enables or disables lockout mode.
func SetLockoutMode(enabled bool) {
	lockoutMu.Lock()
	lockoutMode = enabled
	lockoutMu.Unlock()
}

// Rate limiting for login attempts
var (
	loginAttempts   = make(map[string]*rateLimitEntry)
	loginAttemptsMu sync.RWMutex
)

type rateLimitEntry struct {
	attempts  int
	lastReset time.Time
}

const (
	maxLoginAttempts   = 5
	rateLimitWindow    = 15 * time.Minute
	rateLimitLockout   = 15 * time.Minute
)

// checkRateLimit returns true if the request should be blocked due to rate limiting
func checkRateLimit(identifier string) bool {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	now := time.Now()

	// Periodically clean up stale entries (every 100 checks or so)
	if len(loginAttempts) > 0 && now.UnixNano()%100 == 0 {
		for id, e := range loginAttempts {
			if now.Sub(e.lastReset) > rateLimitWindow+rateLimitLockout {
				delete(loginAttempts, id)
			}
		}
	}

	entry, exists := loginAttempts[identifier]

	if !exists {
		loginAttempts[identifier] = &rateLimitEntry{attempts: 0, lastReset: now}
		return false
	}

	// Reset if window has passed
	if now.Sub(entry.lastReset) > rateLimitWindow {
		entry.attempts = 0
		entry.lastReset = now
		return false
	}

	return entry.attempts >= maxLoginAttempts
}

// recordLoginAttempt records a failed login attempt
func recordLoginAttempt(identifier string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	entry, exists := loginAttempts[identifier]
	if !exists {
		loginAttempts[identifier] = &rateLimitEntry{attempts: 1, lastReset: time.Now()}
		return
	}

	entry.attempts++
}

// clearLoginAttempts clears login attempts after successful login
func clearLoginAttempts(identifier string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, identifier)
}

// CSRF token functions
const csrfTokenLength = 32

// generateCSRFToken creates a new random CSRF token
func generateCSRFToken() (string, error) {
	bytes := make([]byte, csrfTokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// getOrCreateCSRFToken gets existing token from session or creates a new one
func getOrCreateCSRFToken(r *http.Request) string {
	token := sessionManager.GetString(r.Context(), "csrfToken")
	if token != "" {
		return token
	}

	token, err := generateCSRFToken()
	if err != nil {
		log.Printf("Failed to generate CSRF token: %v", err)
		return ""
	}

	sessionManager.Put(r.Context(), "csrfToken", token)
	return token
}

// validateCSRFToken checks if the provided token matches the session token
func validateCSRFToken(r *http.Request, token string) bool {
	sessionToken := sessionManager.GetString(r.Context(), "csrfToken")
	if sessionToken == "" || token == "" {
		log.Printf("[DEBUG] CSRF validation: sessionToken empty=%v, formToken empty=%v", sessionToken == "", token == "")
		return false
	}
	if sessionToken != token {
		log.Printf("[DEBUG] CSRF validation: tokens don't match, session=%s...%s form=%s...%s", sessionToken[:4], sessionToken[len(sessionToken)-4:], token[:4], token[len(token)-4:])
		return false
	}
	return true
}

// InitAuth initializes the authentication system
func InitAuth(sm *scs.SessionManager, lc *auth.LDAPClient, users []string) {
	sessionManager = sm
	ldapClient = lc
	allowedUsers = make(map[string]bool)
	for _, u := range users {
		allowedUsers[strings.ToLower(u)] = true
	}
	log.Printf("Auth initialized with %d allowed users", len(allowedUsers))
}

// InitAdminUsers initializes the admin users list
func InitAdminUsers(users []string) {
	adminUsers = make(map[string]bool)
	for _, u := range users {
		adminUsers[strings.ToLower(u)] = true
	}
	log.Printf("Admin users initialized: %v", users)
}

// IsAdmin checks if a user has admin privileges
func IsAdmin(user *auth.User) bool {
	if user == nil || user.IsAnonymous() {
		return false
	}
	return adminUsers[strings.ToLower(user.SamAccountName)]
}

// InitKioskAuth initializes the kiosk certificate authentication system
func InitKioskAuth(kiosksFile string, trustedProxies []string) error {
	var err error
	kioskManager, err = auth.NewKioskManager(kiosksFile, trustedProxies)
	if err != nil {
		return err
	}
	log.Printf("Kiosk auth initialized with trusted proxies: %v", trustedProxies)
	return nil
}

// GetKioskManager returns the kiosk manager for use in handlers
func GetKioskManager() *auth.KioskManager {
	return kioskManager
}

// IsUserAllowed checks if a username is in the allowed users list
func IsUserAllowed(username string) bool {
	return allowedUsers[strings.ToLower(username)]
}

// SetUserAllowed adds or removes a user from the in-memory allowed users map.
// This is called when the "cameras" permission is toggled in the admin UI
// so the config file doesn't need manual edits for every user.
func SetUserAllowed(username string, allowed bool) {
	u := strings.ToLower(username)
	if allowed {
		allowedUsers[u] = true
		log.Printf("User %s added to allowed users (via permission grant)", u)
	} else {
		delete(allowedUsers, u)
		log.Printf("User %s removed from allowed users (via permission revoke)", u)
	}
}

// GetSessionManager returns the session manager for use in handlers
func GetSessionManager() *scs.SessionManager {
	return sessionManager
}

// GetLDAPClient returns the LDAP client for use in handlers
func GetLDAPClient() *auth.LDAPClient {
	return ldapClient
}

// authenticate is middleware that loads the user from session into the request context
// This should wrap all handlers
func authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// First, check for certificate-based authentication (kiosks)
		if user := checkCertificateAuth(r); user != nil {
			r = contextSetAuthenticatedUser(r, user)
			next(w, r)
			return
		}

		// Fall back to session-based authentication
		userID := sessionManager.GetString(r.Context(), "authenticatedUserID")
		if userID != "" {
			// Create a basic user object with the ID
			// We don't need to fetch full user details from LDAP for every request
			user := &auth.User{
				SamAccountName: userID,
				DisplayName:    sessionManager.GetString(r.Context(), "authenticatedUserName"),
			}
			r = contextSetAuthenticatedUser(r, user)
		}
		next(w, r)
	}
}

// checkCertificateAuth checks for client certificate authentication via X-SSL-* headers
// Only accepts headers from trusted proxies (localhost nginx)
func checkCertificateAuth(r *http.Request) *auth.User {
	// Skip if kiosk auth is not configured
	if kioskManager == nil {
		return nil
	}

	// Extract the remote IP (connection source)
	remoteIP := extractRemoteIP(r)

	// Only accept X-SSL-* headers from trusted proxies
	if !kioskManager.IsTrustedProxy(remoteIP) {
		return nil
	}

	// Check for client certificate verification status
	verifyStatus := r.Header.Get("X-SSL-Client-Verify")
	if verifyStatus != "SUCCESS" {
		return nil
	}

	// Get the client certificate CN
	clientCN := r.Header.Get("X-SSL-Client-CN")
	if clientCN == "" {
		return nil
	}

	// Validate the CN against our kiosk registry
	kiosk, err := kioskManager.ValidateCN(clientCN)
	if err != nil {
		log.Printf("[WARN] Kiosk certificate rejected: cn=%s error=%v", clientCN, err)
		return nil
	}

	log.Printf("[INFO] Kiosk authenticated via certificate: cn=%s name=%s location=%s",
		kiosk.CN, kiosk.DisplayName, kiosk.Location)

	return auth.KioskUser(kiosk)
}

// extractRemoteIP extracts the actual remote IP from the request
// This returns the direct connection IP, not X-Forwarded-For
func extractRemoteIP(r *http.Request) string {
	// r.RemoteAddr is in the format "IP:port" or "[IPv6]:port"
	remoteAddr := r.RemoteAddr

	// Handle IPv6 addresses
	if strings.HasPrefix(remoteAddr, "[") {
		// IPv6: [::1]:12345
		if idx := strings.LastIndex(remoteAddr, "]:"); idx != -1 {
			return remoteAddr[1:idx]
		}
		return remoteAddr
	}

	// IPv4: 127.0.0.1:12345
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}

	return remoteAddr
}

// requireAuth is middleware that redirects to login if not authenticated.
// In lockout mode, non-admin users are force-logged out and shown the login page.
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := contextGetAuthenticatedUser(r)
		if user == nil || user.IsAnonymous() {
			// Store the original URL to redirect back after login (skip non-page paths)
			path := r.URL.Path
			if path != "/favicon.ico" && path != "/" &&
				!strings.HasPrefix(path, "/api/") &&
				!strings.Contains(path, "/partial") &&
				!strings.HasPrefix(path, "/admin/") &&
				!strings.HasPrefix(path, "/cameras/exports") {
				sessionManager.Put(r.Context(), "redirectPath", path)
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// In lockout mode, only admins may proceed
		if IsLockoutMode() && !IsAdmin(user) {
			_ = sessionManager.Destroy(r.Context())
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next(w, r)
	}
}

// Public wraps a handler with session loading but no auth requirement
// Use this for public routes that may still need session access
func Public(h http.HandlerFunc) http.HandlerFunc {
	return authenticate(h)
}

// AdminProtected wraps a handler requiring admin privileges
func AdminProtected(h http.HandlerFunc) http.HandlerFunc {
	return authenticate(requireAuth(requireAdmin(AccessLoggingMiddleware(h))))
}

// requireAdmin is middleware that requires admin privileges
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := contextGetAuthenticatedUser(r)
		if !IsAdmin(user) {
			http.Error(w, "Forbidden - Admin access required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
