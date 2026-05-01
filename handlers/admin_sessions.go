package handlers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

// AdminSessionsHandler serves the sessions tab content
func AdminSessionsHandler(w http.ResponseWriter, r *http.Request) {
	sessions, err := db.GetActiveSessions(sessionActiveSeconds)
	if err != nil {
		log.Printf("Error getting active sessions: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	data := &models.AdminDashboardData{
		Tab:       "sessions",
		Sessions:  sessions,
		UpdatedAt: time.Now(),
	}

	renderTemplate(w, "admin_sessions_content.html", data)
}

// AdminInvalidateSessionHandler handles session invalidation (force logout)
func AdminInvalidateSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionToken := r.FormValue("session_token")
	if sessionToken == "" {
		http.Error(w, "Missing session token", http.StatusBadRequest)
		return
	}

	if err := db.InvalidateSession(sessionToken); err != nil {
		log.Printf("Error invalidating session: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Admin invalidated session: %s", sessionToken)

	// Return updated sessions list
	AdminSessionsHandler(w, r)
}

// HeartbeatHandler handles heartbeat requests from clients
func HeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := contextGetAuthenticatedUser(r)
	if user == nil || user.IsAnonymous() {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get session token from cookie
	sessionToken := ""
	if cookie, err := r.Cookie("camera_dashboard_session"); err == nil {
		sessionToken = cookie.Value
	}
	if sessionToken == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get current page from query param
	currentPage := r.URL.Query().Get("page")
	if currentPage == "" {
		currentPage = r.Referer()
		// Extract just the path from referer
		if idx := strings.Index(currentPage, "://"); idx != -1 {
			if pathIdx := strings.Index(currentPage[idx+3:], "/"); pathIdx != -1 {
				currentPage = currentPage[idx+3+pathIdx:]
			}
		}
	}

	session := &models.ActiveSession{
		SessionToken: sessionToken,
		UserID:       user.SamAccountName,
		UserName:     user.DisplayName,
		Email:        user.Email,
		ClientIP:     extractClientIP(r),
		UserAgent:    truncateString(r.UserAgent(), 500),
		CurrentPage:  currentPage,
	}

	if err := db.UpsertActiveSession(session); err != nil {
		log.Printf("Error updating session heartbeat: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}
