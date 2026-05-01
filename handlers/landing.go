package handlers

import (
	"html/template"
	"net/http"
	"time"

	"cameradashboard/models"
)

var (
	templates       *template.Template
	refreshInterval time.Duration
)

// Init initializes the handlers with templates and config
func Init(tmpl *template.Template, interval int) {
	templates = tmpl
	refreshInterval = time.Duration(interval) * time.Second
}

// SetUserInfoFromRequest sets user info on BaseDashboardData from the request context
func SetUserInfoFromRequest(r *http.Request, base *models.BaseDashboardData) {
	if user := contextGetAuthenticatedUser(r); user != nil {
		base.User = &models.UserInfo{
			Username:    user.SamAccountName,
			DisplayName: user.DisplayName,
		}
		base.IsAdmin = IsAdmin(user)
		base.CSRFToken = getOrCreateCSRFToken(r)

		// Check per-dashboard show-dollars permission (respects impersonation)
		if base.DashboardType != nil && DollarCapableSlugs[base.DashboardType.Slug] {
			dollarSlug := "show-dollars:" + base.DashboardType.Slug
			effectiveUser, impersonating := getEffectiveUserForPermissions(r)
			if base.IsAdmin && !impersonating {
				base.ShowDollars = true
			} else {
				base.ShowDollars = UserHasSlugPermission(effectiveUser, false, dollarSlug)
			}
		}
	}
}

// LandingHandler redirects to the cameras dashboard (this is a cameras-only app)
func LandingHandler(w http.ResponseWriter, r *http.Request) {
	// Only serve landing page for exact "/" path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Cameras-only app — skip landing page and go straight to cameras
	http.Redirect(w, r, "/cameras", http.StatusSeeOther)
}

