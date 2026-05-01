package handlers

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"cameradashboard/db"
	"cameradashboard/models"
)

// AdminPermissionsHandler serves the permissions tab content
func AdminPermissionsHandler(w http.ResponseWriter, r *http.Request) {
	selectedUser := r.URL.Query().Get("user")

	// Get all active AD users
	var users []models.UserPermissions
	var ldapError string

	if ldapClient != nil {
		adUsers, err := ldapClient.GetAllUsersBasic()
		if err != nil {
			log.Printf("Error fetching AD users for permissions: %v", err)
			ldapError = fmt.Sprintf("LDAP error: %v", err)
		} else {
			for _, adUser := range adUsers {
				if !adUser.Enabled || adUser.DisplayName == "" {
					continue
				}
				username := strings.ToLower(adUser.SamAccountName)
				perms, err := db.GetUserPermissions(username)
				if err != nil {
					log.Printf("Error getting permissions for %s: %v", username, err)
					perms = make(map[string]bool)
				}

				users = append(users, models.UserPermissions{
					UserID:      username,
					DisplayName: adUser.DisplayName,
					IsAdmin:     adminUsers[username],
					Granted:     perms,
				})
			}
		}
	} else {
		ldapError = "LDAP client not initialized"
	}

	// Fallback: if LDAP failed, populate from allowedUsers map
	if len(users) == 0 && len(allowedUsers) > 0 {
		if ldapError != "" {
			log.Printf("Permissions: Falling back to allowedUsers list (%d users)", len(allowedUsers))
		}
		for username := range allowedUsers {
			perms, err := db.GetUserPermissions(username)
			if err != nil {
				perms = make(map[string]bool)
			}
			users = append(users, models.UserPermissions{
				UserID:      username,
				DisplayName: username,
				IsAdmin:     adminUsers[username],
				Granted:     perms,
			})
		}
	}

	// Sort users: admins first, then users with camera access, then the rest
	sort.Slice(users, func(i, j int) bool {
		iCam := users[i].Granted["cameras"]
		jCam := users[j].Granted["cameras"]
		// Admins first
		if users[i].IsAdmin != users[j].IsAdmin {
			return users[i].IsAdmin
		}
		// Camera access users next
		if iCam != jCam {
			return iCam
		}
		return users[i].DisplayName < users[j].DisplayName
	})

	// Default to first non-admin user if no user selected
	if selectedUser == "" && len(users) > 0 {
		for _, u := range users {
			if !u.IsAdmin {
				selectedUser = u.UserID
				break
			}
		}
		if selectedUser == "" {
			selectedUser = users[0].UserID
		}
	}

	data := &models.PermissionsPageData{
		Users:         users,
		Categories:    GetPermissionCategories(),
		CamerasSlug:   CamerasSlug,
		DollarCapable: DollarCapableSlugs,
		LDAPError:     ldapError,
		SelectedUser:  selectedUser,
		Tab:           "permissions",
	}

	renderTemplate(w, "admin_permissions_content.html", data)
}

// AdminPermissionToggleHandler toggles a single permission
func AdminPermissionToggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.FormValue("user_id")
	slug := r.FormValue("slug")
	action := r.FormValue("action") // "grant" or "revoke"

	if userID == "" || slug == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	adminID := getAuthUserID(r)

	var err error
	if action == "grant" {
		err = db.GrantPermission(userID, slug, adminID)
	} else {
		err = db.RevokePermission(userID, slug)
	}

	if err != nil {
		log.Printf("Error toggling permission %s for %s: %v", slug, userID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Admin %s %sd %s permission for %s", adminID, action, slug, userID)

	// Sync allowed users: granting "cameras" lets user log in, revoking removes login access
	if slug == "cameras" {
		if action == "grant" {
			SetUserAllowed(userID, true)
		} else if !adminUsers[strings.ToLower(userID)] {
			SetUserAllowed(userID, false)
		}
	}

	// Return updated permissions tab
	r.URL.RawQuery = "user=" + userID
	AdminPermissionsHandler(w, r)
}

// AdminGrantAllHandler grants all permissions to a user
func AdminGrantAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	adminID := getAuthUserID(r)

	if err := db.GrantAllPermissions(userID, GetAllSlugStrings(), adminID); err != nil {
		log.Printf("Error granting all permissions for %s: %v", userID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Admin %s granted all permissions to %s", adminID, userID)
	SetUserAllowed(userID, true)

	r.URL.RawQuery = "user=" + userID
	AdminPermissionsHandler(w, r)
}

// AdminRevokeAllHandler revokes all permissions from a user
func AdminRevokeAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	adminID := getAuthUserID(r)

	if err := db.RevokeAllPermissions(userID); err != nil {
		log.Printf("Error revoking all permissions for %s: %v", userID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Admin %s revoked all permissions from %s", adminID, userID)
	if !adminUsers[strings.ToLower(userID)] {
		SetUserAllowed(userID, false)
	}

	r.URL.RawQuery = "user=" + userID
	AdminPermissionsHandler(w, r)
}

// AdminCameraPermissionsHandler serves the camera permissions panel for a user
func AdminCameraPermissionsHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user")
	if userID == "" {
		http.Error(w, "Missing user", http.StatusBadRequest)
		return
	}

	// Look up display name
	displayName := userID
	if ldapClient != nil {
		if adUser, err := ldapClient.GetUser(userID); err == nil && adUser != nil {
			displayName = adUser.DisplayName
		}
	}

	// Get NVRs and presets from camera dashboard
	nvrs := GetCameraNVRs()
	presets := GetCameraPresets()

	// Get user's camera permissions
	camPerms, err := db.GetUserCameraPermissions(userID)
	if err != nil {
		log.Printf("Error getting camera permissions for %s: %v", userID, err)
		camPerms = make(map[string]map[int]bool)
	}

	// Get user's preset permissions
	presetPerms, err := db.GetUserPresetPermissions(userID)
	if err != nil {
		log.Printf("Error getting preset permissions for %s: %v", userID, err)
		presetPerms = make(map[string]bool)
	}

	// Build NVR permission data
	var permNVRs []models.CameraPermNVR
	for _, nvr := range nvrs {
		pn := models.CameraPermNVR{
			ID:          nvr.ID,
			Name:        nvr.Name,
			Channels:    nvr.Channels,
			Grants:      make(map[int]bool),
			CameraNames: make(map[int]string),
		}

		if channels, ok := camPerms[nvr.ID]; ok {
			pn.AllGrant = channels[0]
			for ch, granted := range channels {
				if ch > 0 {
					pn.Grants[ch] = granted
				}
			}
		}

		// Get camera names
		for ch := 1; ch <= nvr.Channels; ch++ {
			pn.CameraNames[ch] = GetChannelNameExported(nvr.ID, ch)
		}

		permNVRs = append(permNVRs, pn)
	}

	// Build preset permission data
	var permPresets []models.CameraPermPreset
	for _, preset := range presets {
		permPresets = append(permPresets, models.CameraPermPreset{
			ID:      preset.ID,
			Name:    preset.Name,
			Granted: presetPerms[preset.ID],
		})
	}

	data := &models.CameraPermissionsPageData{
		UserID:      userID,
		DisplayName: displayName,
		NVRs:        permNVRs,
		Presets:     permPresets,
		Tab:         "camera-permissions",
	}

	renderTemplate(w, "admin_camera_permissions_content.html", data)
}

// AdminCameraPermissionToggleHandler toggles a camera or preset permission
func AdminCameraPermissionToggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID := r.FormValue("user_id")
	action := r.FormValue("action") // "grant" or "revoke"
	permType := r.FormValue("type") // "camera", "preset", "grant-all-nvrs", "revoke-all-cameras"

	if userID == "" {
		http.Error(w, "Missing user_id", http.StatusBadRequest)
		return
	}

	adminID := getAuthUserID(r)

	var err error
	switch permType {
	case "camera":
		nvrID := r.FormValue("nvr_id")
		channelStr := r.FormValue("channel")
		channel := 0
		if channelStr != "" {
			for _, c := range channelStr {
				if c >= '0' && c <= '9' {
					channel = channel*10 + int(c-'0')
				}
			}
		}
		if action == "grant" {
			err = db.GrantCameraPermission(userID, nvrID, channel)
		} else {
			err = db.RevokeCameraPermission(userID, nvrID, channel)
		}
		if err == nil {
			log.Printf("Admin %s %sd camera %s ch%d for %s", adminID, action, nvrID, channel, userID)
		}

	case "preset":
		presetID := r.FormValue("preset_id")
		if action == "grant" {
			err = db.GrantPresetPermission(userID, presetID)
			// Auto-grant all cameras in the preset
			if err == nil {
				for _, preset := range GetCameraPresets() {
					if preset.ID == presetID {
						for _, entry := range preset.Cameras {
							if grantErr := db.GrantCameraPermission(userID, entry.NVRID, entry.Channel); grantErr != nil {
								log.Printf("Error auto-granting camera %s ch%d for preset %s: %v", entry.NVRID, entry.Channel, presetID, grantErr)
							}
						}
						break
					}
				}
			}
		} else {
			err = db.RevokePresetPermission(userID, presetID)
			// Also revoke the individual camera grants that were auto-granted
			if err == nil {
				for _, preset := range GetCameraPresets() {
					if preset.ID == presetID {
						for _, entry := range preset.Cameras {
							if revokeErr := db.RevokeCameraPermission(userID, entry.NVRID, entry.Channel); revokeErr != nil {
								log.Printf("Error auto-revoking camera %s ch%d for preset %s: %v", entry.NVRID, entry.Channel, presetID, revokeErr)
							}
						}
						break
					}
				}
			}
		}
		if err == nil {
			log.Printf("Admin %s %sd preset %s for %s", adminID, action, presetID, userID)
		}

	case "grant-all-nvrs":
		nvrs := GetCameraNVRs()
		nvrIDs := make([]string, len(nvrs))
		for i, nvr := range nvrs {
			nvrIDs[i] = nvr.ID
		}
		err = db.GrantAllNVRs(userID, nvrIDs)
		if err == nil {
			log.Printf("Admin %s granted all NVRs to %s", adminID, userID)
		}

	case "revoke-all-cameras":
		err = db.RevokeAllCameraPermissions(userID)
		if err == nil {
			log.Printf("Admin %s revoked all camera permissions from %s", adminID, userID)
		}
	}

	if err != nil {
		log.Printf("Error toggling camera permission for %s: %v", userID, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Return updated camera permissions panel
	r.URL.RawQuery = "user=" + userID
	AdminCameraPermissionsHandler(w, r)
}
