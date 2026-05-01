package handlers

import (
	"log"
	"net/http"
	"strings"

	"cameradashboard/db"
	"cameradashboard/models"
)

// AllDashboardSlugs is the ordered list of all permission slugs for Camera dashboards.
var AllDashboardSlugs = []models.DashboardSlugInfo{
	{Slug: "cameras", Name: "Cameras"},
}

// DollarCapableSlugs lists dashboards that display dollar values.
var DollarCapableSlugs = map[string]bool{}

// CamerasSlug is handled separately in the permissions UI with its own card
var CamerasSlug = models.DashboardSlugInfo{Slug: "cameras", Name: "Cameras"}

// GetPermissionCategories returns all slugs as a single flat group
func GetPermissionCategories() []models.PermissionCategory {
	return []models.PermissionCategory{
		{Name: "Dashboards", Dashboards: AllDashboardSlugs},
	}
}

// GetAllSlugStrings returns just the slug strings for grant-all operations
func GetAllSlugStrings() []string {
	slugs := make([]string, 0, len(AllDashboardSlugs))
	for _, s := range AllDashboardSlugs {
		slugs = append(slugs, s.Slug)
	}
	return slugs
}

// pathToSlug maps a URL path to its permission slug.
func pathToSlug(path string) string {
	prefixes := []struct {
		prefix string
		slug   string
	}{
		{"/cameras", "cameras"},
	}

	for _, p := range prefixes {
		if path == p.prefix || strings.HasPrefix(path, p.prefix+"/") || strings.HasPrefix(path, p.prefix+"?") {
			return p.slug
		}
	}

	// API endpoints
	apiMap := map[string]string{
		"/api/cameras": "cameras",
	}

	for prefix, slug := range apiMap {
		if strings.HasPrefix(path, prefix) {
			return slug
		}
	}

	return ""
}

// getEffectiveUserForPermissions returns the user ID to use for permission checks.
// If an admin is impersonating another user, returns the impersonated user's ID.
// Otherwise returns the real user's SamAccountName.
func getEffectiveUserForPermissions(r *http.Request) (userID string, isImpersonating bool) {
	user := contextGetAuthenticatedUser(r)
	if user == nil {
		return "", false
	}

	// Only admins can impersonate
	if IsAdmin(user) && sessionManager != nil {
		impersonated := sessionManager.GetString(r.Context(), "impersonateUserID")
		if impersonated != "" {
			return impersonated, true
		}
	}

	return user.SamAccountName, false
}

// requireDashboardPermission is middleware that checks per-dashboard permissions.
// Admin users bypass all checks unless impersonating. Returns 403 for denied access.
func requireDashboardPermission(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip permission check for paths that don't map to a dashboard
		slug := pathToSlug(path)
		if slug == "" {
			next(w, r)
			return
		}

		user := contextGetAuthenticatedUser(r)
		if user == nil {
			next(w, r)
			return
		}

		// Check if impersonating — use impersonated user's permissions
		effectiveUser, impersonating := getEffectiveUserForPermissions(r)

		// Admin users bypass all permission checks (unless impersonating)
		if IsAdmin(user) && !impersonating {
			next(w, r)
			return
		}

		// Check permission for effective user
		allowed, err := db.UserHasPermission(effectiveUser, slug)
		if err != nil {
			log.Printf("Error checking permission for %s on %s: %v", effectiveUser, slug, err)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		if !allowed {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			data := struct {
				Path string
			}{Path: path}
			if err := templates.ExecuteTemplate(w, "access_denied.html", data); err != nil {
				log.Printf("Template error rendering access_denied: %v", err)
				http.Error(w, "Access Denied", http.StatusForbidden)
			}
			return
		}

		next(w, r)
	}
}

// UserHasSlugPermission checks if a user has permission for a specific slug.
// Admin users always return true. Exported for use in landing page filtering.
func UserHasSlugPermission(userID string, isAdmin bool, slug string) bool {
	if isAdmin {
		return true
	}
	allowed, err := db.UserHasPermission(userID, slug)
	if err != nil {
		log.Printf("Error checking permission for %s on %s: %v", userID, slug, err)
		return false
	}
	return allowed
}
