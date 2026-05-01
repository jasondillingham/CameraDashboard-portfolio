package handlers

import (
	"encoding/json"
	"net/http"
)

var appVersion string

// SetVersion sets the application version for the version endpoint
func SetVersion(version string) {
	appVersion = version
}

// GetVersion returns the current application version
func GetVersion() string {
	return appVersion
}

// VersionResponse is the JSON response for the version endpoint
type VersionResponse struct {
	Version string `json:"version"`
}

// VersionHandler returns the current application version
func VersionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	response := VersionResponse{Version: appVersion}
	json.NewEncoder(w).Encode(response)
}
