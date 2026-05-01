package handlers

import (
	"log"
	"net/http"
	"strings"
)

// LoginData holds data for the login template
type LoginData struct {
	Error     string
	Username  string
	CSRFToken string
}

// LoginHandler handles GET and POST requests to /login
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		loginGet(w, r)
	case http.MethodPost:
		loginPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// loginGet renders the login form
func loginGet(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to cameras
	user := contextGetAuthenticatedUser(r)
	if user != nil && !user.IsAnonymous() {
		http.Redirect(w, r, "/cameras", http.StatusSeeOther)
		return
	}

	data := LoginData{
		CSRFToken: getOrCreateCSRFToken(r),
	}
	if IsLockoutMode() {
		data.Error = "Camera Dashboard is temporarily unavailable for maintenance."
	}

	err := templates.ExecuteTemplate(w, "login.html", data)
	if err != nil {
		log.Printf("Error rendering login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// loginPost handles the login form submission
func loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderLoginError(w, r, "Invalid form submission", "")
		return
	}

	// Validate CSRF token
	csrfToken := r.FormValue("csrf_token")
	if !validateCSRFToken(r, csrfToken) {
		log.Printf("CSRF token validation failed")
		renderLoginError(w, r, "Invalid form submission. Please try again.", "")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	// Check rate limiting by IP address (use direct connection IP only)
	clientIP := extractRemoteIP(r)

	if checkRateLimit(clientIP) {
		log.Printf("Rate limit exceeded for IP %s", clientIP)
		renderLoginError(w, r, "Too many login attempts. Please try again later.", username)
		return
	}

	if username == "" || password == "" {
		renderLoginError(w, r, "Username and password are required", username)
		return
	}

	// Authenticate against Active Directory
	userID, err := ldapClient.AuthenticateUser(username, password)
	if err != nil {
		log.Printf("Authentication failed for user %s: %v", username, err)
		recordLoginAttempt(clientIP)
		renderLoginError(w, r, "Invalid username or password", username)
		return
	}

	// Check if user is in the allowed users list
	if !IsUserAllowed(userID) {
		log.Printf("User %s authenticated but not in allowed users list", userID)
		recordLoginAttempt(clientIP)
		renderLoginError(w, r, "You are not authorized to access this application", username)
		return
	}

	// In lockout mode, only admins may log in
	if IsLockoutMode() && !adminUsers[strings.ToLower(userID)] {
		log.Printf("User %s blocked by lockout mode", userID)
		renderLoginError(w, r, "Camera Dashboard is temporarily unavailable for maintenance.", username)
		return
	}

	// Get user details for display name
	user, err := ldapClient.GetUser(userID)
	if err != nil {
		log.Printf("Failed to get user details for %s: %v", userID, err)
		// Continue with just the username
		user = nil
	}

	// Renew session token to prevent session fixation
	if err := sessionManager.RenewToken(r.Context()); err != nil {
		log.Printf("Failed to renew session token: %v", err)
		renderLoginError(w, r, "Session error, please try again", username)
		return
	}

	// Store user info in session
	sessionManager.Put(r.Context(), "authenticatedUserID", userID)
	if user != nil {
		sessionManager.Put(r.Context(), "authenticatedUserName", user.DisplayName)
	} else {
		sessionManager.Put(r.Context(), "authenticatedUserName", userID)
	}

	// Clear rate limit on successful login
	clearLoginAttempts(clientIP)

	// Rotate CSRF token after successful authentication
	sessionManager.Remove(r.Context(), "csrfToken")

	log.Printf("User %s logged in successfully", userID)

	// Redirect to original path or cameras dashboard
	redirectPath := sessionManager.PopString(r.Context(), "redirectPath")
	if redirectPath == "" || redirectPath == "/login" || redirectPath == "/" || redirectPath == "/favicon.ico" {
		redirectPath = "/cameras"
	}

	http.Redirect(w, r, redirectPath, http.StatusSeeOther)
}

// renderLoginError renders the login page with an error message
func renderLoginError(w http.ResponseWriter, r *http.Request, errorMsg, username string) {
	data := LoginData{
		Error:     errorMsg,
		Username:  username,
		CSRFToken: getOrCreateCSRFToken(r),
	}

	w.WriteHeader(http.StatusUnauthorized)
	err := templates.ExecuteTemplate(w, "login.html", data)
	if err != nil {
		log.Printf("Error rendering login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// LogoutHandler handles POST requests to /logout
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get user ID for logging before destroying session
	userID := sessionManager.GetString(r.Context(), "authenticatedUserID")

	// Destroy the session
	if err := sessionManager.Destroy(r.Context()); err != nil {
		log.Printf("Failed to destroy session: %v", err)
	}

	log.Printf("User %s logged out", userID)

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
