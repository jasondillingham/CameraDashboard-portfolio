package handlers

import (
	"context"
	"net/http"

	"cameradashboard/internal/auth"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	authenticatedUserKey contextKey = "authenticatedUser"
)

// contextSetAuthenticatedUser stores the authenticated user in the request context
func contextSetAuthenticatedUser(r *http.Request, user *auth.User) *http.Request {
	ctx := context.WithValue(r.Context(), authenticatedUserKey, user)
	return r.WithContext(ctx)
}

// contextGetAuthenticatedUser retrieves the authenticated user from the request context
func contextGetAuthenticatedUser(r *http.Request) *auth.User {
	user, ok := r.Context().Value(authenticatedUserKey).(*auth.User)
	if !ok {
		return nil
	}
	return user
}

// getAuthUserID returns the SamAccountName of the authenticated user, or "" if not authenticated.
func getAuthUserID(r *http.Request) string {
	if user := contextGetAuthenticatedUser(r); user != nil {
		return user.SamAccountName
	}
	return ""
}
