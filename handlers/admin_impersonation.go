package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
)

// ImpersonateHandler sets impersonation for the current admin session
func ImpersonateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate CSRF token
	csrfToken := r.FormValue("csrf_token")
	if !validateCSRFToken(r, csrfToken) {
		log.Printf("CSRF token validation failed on impersonate")
		http.Error(w, "Invalid request", http.StatusForbidden)
		return
	}

	userID := strings.ToLower(strings.TrimSpace(r.FormValue("user_id")))
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	// Validate user exists in AD
	if ldapClient != nil {
		adUser, err := ldapClient.GetUser(userID)
		if err != nil || adUser == nil || !adUser.Enabled {
			http.Error(w, "User not found or disabled", http.StatusBadRequest)
			return
		}
	}

	admin := contextGetAuthenticatedUser(r)
	log.Printf("Admin %s started impersonating %s", admin.SamAccountName, userID)

	sessionManager.Put(r.Context(), "impersonateUserID", userID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// StopImpersonateHandler clears impersonation for the current admin session
func StopImpersonateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate CSRF token
	csrfToken := r.FormValue("csrf_token")
	if !validateCSRFToken(r, csrfToken) {
		log.Printf("CSRF token validation failed on stop impersonate")
		http.Error(w, "Invalid request", http.StatusForbidden)
		return
	}

	admin := contextGetAuthenticatedUser(r)
	impersonated := sessionManager.GetString(r.Context(), "impersonateUserID")
	if impersonated != "" {
		log.Printf("Admin %s stopped impersonating %s", admin.SamAccountName, impersonated)
	}

	sessionManager.Remove(r.Context(), "impersonateUserID")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ImpersonateStatusHandler returns the current impersonation status as JSON
func ImpersonateStatusHandler(w http.ResponseWriter, r *http.Request) {
	user := contextGetAuthenticatedUser(r)
	result := map[string]interface{}{
		"active":      false,
		"userId":      "",
		"displayName": "",
		"isAdmin":     false,
	}

	if user != nil {
		result["isAdmin"] = IsAdmin(user)
	}

	if user != nil && IsAdmin(user) && sessionManager != nil {
		impersonated := sessionManager.GetString(r.Context(), "impersonateUserID")
		if impersonated != "" {
			result["active"] = true
			result["userId"] = impersonated

			// Look up display name
			displayName := impersonated
			if ldapClient != nil {
				if ldapUser, err := ldapClient.GetUser(impersonated); err == nil && ldapUser != nil {
					displayName = ldapUser.DisplayName
				}
			}
			result["displayName"] = displayName
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// AllowedUsersHandler returns the list of active AD users as JSON (admin-only in handler)
func AllowedUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := contextGetAuthenticatedUser(r)
	if !IsAdmin(user) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	type userEntry struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
	}

	var users []userEntry

	if ldapClient != nil {
		adUsers, err := ldapClient.GetAllUsersBasic()
		if err != nil {
			log.Printf("Error fetching AD users for View As: %v", err)
			http.Error(w, "Failed to fetch users", http.StatusInternalServerError)
			return
		}
		for _, adUser := range adUsers {
			// Only include enabled accounts with a display name
			if !adUser.Enabled || adUser.DisplayName == "" {
				continue
			}
			// Skip admin users (impersonating another admin is not useful)
			if adminUsers[strings.ToLower(adUser.SamAccountName)] {
				continue
			}
			users = append(users, userEntry{
				UserID:      adUser.SamAccountName,
				DisplayName: adUser.DisplayName,
			})
		}
	}

	// Sort by display name
	sort.Slice(users, func(i, j int) bool {
		return users[i].DisplayName < users[j].DisplayName
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}
