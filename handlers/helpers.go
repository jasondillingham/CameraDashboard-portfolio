package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"cameradashboard/db"
	"cameradashboard/models"
)

// parseBranchFromPath extracts a branch slug from a URL path after trimming the base prefix.
// Returns the branch if found, nil if path is empty, or sends 404 and returns nil if not found.
// The second return value indicates if the handler should continue (true) or return early (false).
func parseBranchFromPath(w http.ResponseWriter, r *http.Request, basePrefix string, excludePaths ...string) (*models.Branch, bool) {
	// Trim the base prefix and clean up the path
	path := strings.TrimPrefix(r.URL.Path, basePrefix)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")

	// Check if path matches any excluded paths (like "week", "partial", etc.)
	for _, excluded := range excludePaths {
		if path == excluded || strings.HasPrefix(path, excluded+"/") {
			return nil, true
		}
	}

	// Empty path means no branch filter
	if path == "" {
		return nil, true
	}

	// Try to find the branch
	branch := db.GetBranchBySlug(path)
	if branch == nil {
		http.NotFound(w, r)
		return nil, false
	}

	return branch, true
}

// getBranchFromQuery extracts a branch from the "branch" query parameter.
// Returns nil if the parameter is empty or the branch is not found.
func getBranchFromQuery(r *http.Request) *models.Branch {
	branchSlug := r.URL.Query().Get("branch")
	if branchSlug == "" {
		return nil
	}
	return db.GetBranchBySlug(branchSlug)
}

// getQueryParam returns a query parameter value or a default if empty.
func getQueryParam(r *http.Request, key, defaultValue string) string {
	if val := r.URL.Query().Get(key); val != "" {
		return val
	}
	return defaultValue
}

// renderTemplate renders an HTML template with the given data.
// Logs and sends a 500 error if rendering fails.
func renderTemplate(w http.ResponseWriter, templateName string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, templateName, data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// renderJSON encodes data as JSON and writes it to the response.
// Logs and sends a 500 error if encoding fails.
func renderJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("JSON encoding error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// parseStreamID extracts the NVR ID and channel number from a stream ID.
// Stream IDs follow the format "{nvrID}_cam{channel}" or "{nvrID}_cam{channel}_sub".
// Returns an error if the format is invalid or the channel can't be parsed.
func parseStreamID(streamID string) (nvrID string, channel int, err error) {
	// Strip _sub suffix if present
	baseName := strings.TrimSuffix(streamID, "_sub")

	parts := strings.Split(baseName, "_cam")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid stream ID format: %s", streamID)
	}
	nvrID = parts[0]

	for i, c := range parts[1] {
		if c >= '0' && c <= '9' {
			channel = channel*10 + int(c-'0')
		} else if i > 0 {
			break
		}
	}

	if channel == 0 {
		return "", 0, fmt.Errorf("invalid channel in stream ID: %s", streamID)
	}

	return nvrID, channel, nil
}

// DashboardDataSetter is implemented by types that embed BaseDashboardData
type DashboardDataSetter interface {
	SetDashboardInfo(slug string, dbInfo *models.DatabaseInfo)
}

// setDashboardInfo sets common dashboard fields on data structures.
// Works with any type that embeds BaseDashboardData.
func setDashboardInfo(data interface{}, slug string) {
	if setter, ok := data.(DashboardDataSetter); ok {
		setter.SetDashboardInfo(slug, db.GetDatabaseInfo())
	}
}

// extractPathSegment extracts a path segment after a base path.
// e.g., extractPathSegment("/pick-tickets/week/hq", "/pick-tickets") returns "week/hq"
func extractPathSegment(fullPath, basePath string) string {
	path := strings.TrimPrefix(fullPath, basePath)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	return path
}
