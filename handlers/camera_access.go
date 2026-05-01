package handlers

import (
	"log"
	"net/http"

	"cameradashboard/db"
	"cameradashboard/models"
)

// shouldFilterCameras returns true if camera-level permission filtering should apply.
// Admin users bypass unless impersonating.
func shouldFilterCameras(r *http.Request) (userID string, filter bool) {
	user := contextGetAuthenticatedUser(r)
	if user == nil {
		return "", false
	}

	effectiveUser, impersonating := getEffectiveUserForPermissions(r)

	// Admin users bypass all permission checks (unless impersonating)
	if IsAdmin(user) && !impersonating {
		return "", false
	}

	return effectiveUser, true
}

// filterNVRsByPermission filters NVRs to only those the user has any grant for.
// Loads the user's full camera permission map once, then filters in-memory.
func filterNVRsByPermission(nvrs []models.NVR, userID string) []models.NVR {
	camPerms, err := db.GetUserCameraPermissions(userID)
	if err != nil {
		log.Printf("Error loading camera permissions for %s: %v", userID, err)
		return nil
	}
	var filtered []models.NVR
	for _, nvr := range nvrs {
		if channels, ok := camPerms[nvr.ID]; ok && len(channels) > 0 {
			filtered = append(filtered, nvr)
		}
	}
	return filtered
}

// filterCamerasByPermission filters cameras to only those the user has access to.
// Loads the user's full camera permission map once, then filters in-memory.
func filterCamerasByPermission(cameras []models.Camera, userID, nvrID string) []models.Camera {
	camPerms, err := db.GetUserCameraPermissions(userID)
	if err != nil {
		log.Printf("Error loading camera permissions for %s: %v", userID, err)
		return nil
	}
	channels, ok := camPerms[nvrID]
	if !ok {
		return nil
	}
	// channel=0 means all cameras
	allAccess := channels[0]
	var filtered []models.Camera
	for _, cam := range cameras {
		if allAccess || channels[cam.Channel] {
			filtered = append(filtered, cam)
		}
	}
	return filtered
}

// filterPresetsByPermission filters presets to only those the user has an explicit preset grant for.
// Loads the user's full preset permission map once, then filters in-memory.
func filterPresetsByPermission(presets []models.CameraDashboardPreset, userID string) []models.CameraDashboardPreset {
	presetPerms, err := db.GetUserPresetPermissions(userID)
	if err != nil {
		log.Printf("Error loading preset permissions for %s: %v", userID, err)
		return nil
	}
	var filtered []models.CameraDashboardPreset
	for _, preset := range presets {
		if presetPerms[preset.ID] {
			filtered = append(filtered, preset)
		}
	}
	return filtered
}

// checkCameraAccess verifies a user can view a specific camera, returns false if denied
func checkCameraAccess(w http.ResponseWriter, r *http.Request, nvrID string, channel int) bool {
	userID, filter := shouldFilterCameras(r)
	if !filter {
		return true
	}

	hasAccess, err := db.UserHasCameraAccess(userID, nvrID, channel)
	if err != nil {
		log.Printf("Error checking camera access for %s on %s ch%d: %v", userID, nvrID, channel, err)
		return false // fail closed
	}

	if !hasAccess {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		data := struct{ Path string }{Path: r.URL.Path}
		if err := templates.ExecuteTemplate(w, "access_denied.html", data); err != nil {
			http.Error(w, "Access Denied", http.StatusForbidden)
		}
		return false
	}
	return true
}
