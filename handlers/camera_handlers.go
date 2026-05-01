package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

// CameraDashboardHandler serves the main camera dashboard (NVR list)
func CameraDashboardHandler(w http.ResponseWriter, r *http.Request) {
	cameraNVRsMu.RLock()
	nvrs := make([]models.NVR, len(cameraNVRs))
	copy(nvrs, cameraNVRs)
	cameraNVRsMu.RUnlock()

	presets := cameraDashboardPresets

	// Filter by camera permissions
	if userID, filter := shouldFilterCameras(r); filter {
		nvrs = filterNVRsByPermission(nvrs, userID)
		presets = filterPresetsByPermission(presets, userID)
	}

	data := models.CameraDashboardData{
		View:             "nvrs",
		NVRs:             nvrs,
		DashboardPresets: presets,
		Go2RTCBaseURL:    go2rtcProxyBaseURL,
		Go2RTCDirectURL:  go2rtcBaseURL,
		UpdatedAt:        time.Now(),
		RefreshInterval:  int(refreshInterval.Seconds()),
	}
	data.DashboardType = models.GetDashboardTypeBySlug("cameras")
	data.DashboardTypes = models.DashboardTypes
	SetUserInfoFromRequest(r, &data.BaseDashboardData)

	renderTemplate(w, "camera_dashboard.html", data)
}

// CameraGridHandler shows a multi-camera grid dashboard preset
func CameraGridHandler(w http.ResponseWriter, r *http.Request) {
	presetID := strings.TrimPrefix(r.URL.Path, "/cameras/dashboard/")
	presetID = strings.TrimSuffix(presetID, "/")

	if presetID == "" {
		http.Redirect(w, r, "/cameras", http.StatusFound)
		return
	}

	// Find the matching preset
	var preset *models.CameraDashboardPreset
	for i := range cameraDashboardPresets {
		if cameraDashboardPresets[i].ID == presetID {
			p := cameraDashboardPresets[i]
			preset = &p
			break
		}
	}

	if preset == nil {
		http.Error(w, "Dashboard preset not found", http.StatusNotFound)
		return
	}

	// Check camera permissions for preset
	if userID, filter := shouldFilterCameras(r); filter {
		allowed := filterPresetsByPermission([]models.CameraDashboardPreset{*preset}, userID)
		if len(allowed) == 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			data := struct{ Path string }{Path: r.URL.Path}
			if err := templates.ExecuteTemplate(w, "access_denied.html", data); err != nil {
				http.Error(w, "Access Denied", http.StatusForbidden)
			}
			return
		}
	}

	// Build a map of NVR ID -> NVR for lookups
	cameraNVRsMu.RLock()
	nvrMap := make(map[string]*models.NVR, len(cameraNVRs))
	for i := range cameraNVRs {
		nvr := cameraNVRs[i]
		nvrMap[nvr.ID] = &nvr
	}
	cameraNVRsMu.RUnlock()

	// Resolve each camera entry to a Camera struct with sub-stream name
	gridCameras := make([]models.Camera, 0, len(preset.Cameras))
	for _, entry := range preset.Cameras {
		nvr, ok := nvrMap[entry.NVRID]
		if !ok {
			continue
		}
		cam := models.Camera{
			ID:          nvr.GetStreamName(entry.Channel),
			Name:        getChannelName(entry.NVRID, entry.Channel),
			Channel:     entry.Channel,
			NVRID:       nvr.ID,
			NVRName:     nvr.Name,
			StreamURL:   nvr.GetSubStreamName(entry.Channel), // sub-stream for grid bandwidth
			Go2RTCProxy: nvr.Go2RTCProxyPath(),
		}
		gridCameras = append(gridCameras, cam)
	}

	data := models.CameraDashboardData{
		View:          "grid",
		CurrentPreset: preset,
		GridCameras:   gridCameras,
		Go2RTCBaseURL:   go2rtcProxyBaseURL,
		Go2RTCDirectURL: go2rtcBaseURL,
		UpdatedAt:     time.Now(),
	}
	data.DashboardType = models.GetDashboardTypeBySlug("cameras")
	data.DashboardTypes = models.DashboardTypes
	SetUserInfoFromRequest(r, &data.BaseDashboardData)

	renderTemplate(w, "camera_dashboard.html", data)
}

// CameraListHandler shows cameras for a specific NVR
func CameraListHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cameras/nvr/")
	nvrID := strings.TrimSuffix(path, "/")

	if nvrID == "" {
		http.Redirect(w, r, "/cameras", http.StatusFound)
		return
	}

	selectedNVR := findNVRByID(nvrID)
	if selectedNVR == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	cameras := buildCameraList(selectedNVR)

	// Filter cameras by permission
	if userID, filter := shouldFilterCameras(r); filter {
		// First check the user has any access to this NVR
		hasAccess, err := db.UserHasNVRAccess(userID, selectedNVR.ID)
		if err != nil {
			log.Printf("Error checking NVR access: %v", err)
		} else if !hasAccess {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			data := struct{ Path string }{Path: r.URL.Path}
			if tmplErr := templates.ExecuteTemplate(w, "access_denied.html", data); tmplErr != nil {
				http.Error(w, "Access Denied", http.StatusForbidden)
			}
			return
		}
		cameras = filterCamerasByPermission(cameras, userID, selectedNVR.ID)
	}

	// Pagination: 16 cameras per page
	const pageSize = 16
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	totalPages := (len(cameras) + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if endIdx > len(cameras) {
		endIdx = len(cameras)
	}
	pageCameras := cameras[startIdx:endIdx]

	data := models.CameraDashboardData{
		View:            "cameras",
		CurrentNVR:      selectedNVR,
		Cameras:         pageCameras,
		Go2RTCBaseURL:   selectedNVR.Go2RTCProxyPath(),
		Go2RTCDirectURL: nvrGo2RTCURL(selectedNVR),
		UpdatedAt:       time.Now(),
		RefreshInterval: int(refreshInterval.Seconds()),
		CurrentPage:     page,
		TotalPages:      totalPages,
		PageSize:        pageSize,
	}
	data.DashboardType = models.GetDashboardTypeBySlug("cameras")
	data.DashboardTypes = models.DashboardTypes
	SetUserInfoFromRequest(r, &data.BaseDashboardData)

	renderTemplate(w, "camera_dashboard.html", data)
}

// CameraViewHandler shows the live stream for a camera
func CameraViewHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cameras/view/")
	streamID := strings.TrimSuffix(path, "/")

	if streamID == "" {
		http.Redirect(w, r, "/cameras", http.StatusFound)
		return
	}

	nvrID, channel, err := parseStreamID(streamID)
	if err != nil {
		http.Error(w, "Invalid stream ID", http.StatusBadRequest)
		return
	}

	selectedNVR := findNVRByID(nvrID)
	if selectedNVR == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	if channel > selectedNVR.Channels {
		http.Error(w, "Invalid channel", http.StatusBadRequest)
		return
	}

	// Check camera-level permission
	if !checkCameraAccess(w, r, nvrID, channel) {
		return
	}

	mainStreamName := selectedNVR.GetStreamName(channel)
	subStreamName := selectedNVR.GetSubStreamName(channel)

	// Default to sub stream, allow ?quality=main to override
	activeStream := subStreamName
	if r.URL.Query().Get("quality") == "main" {
		activeStream = mainStreamName
	}

	camera := &models.Camera{
		ID:      mainStreamName,
		Name:    getChannelName(selectedNVR.ID, channel),
		Channel: channel,
		NVRID:   selectedNVR.ID,
		NVRName: selectedNVR.Name,
	}

	data := models.CameraDashboardData{
		View:            "live",
		CurrentNVR:      selectedNVR,
		CurrentCamera:   camera,
		StreamName:      activeStream,
		MainStreamName:  mainStreamName,
		SubStreamName:   subStreamName,
		Go2RTCBaseURL:   selectedNVR.Go2RTCProxyPath(),
		Go2RTCDirectURL: nvrGo2RTCURL(selectedNVR),
		UpdatedAt:       time.Now(),
		RefreshInterval: 0,
	}

	// Set back navigation from Referer or ?back= query param
	// Only accept local /cameras/ paths to prevent open redirect
	if back := r.URL.Query().Get("back"); back != "" &&
		(strings.HasPrefix(back, "/cameras/dashboard/") || strings.HasPrefix(back, "/cameras/nvr/")) {
		data.BackURL = back
	} else if ref := r.Referer(); ref != "" {
		if u, err := url.Parse(ref); err == nil {
			p := u.Path
			if strings.HasPrefix(p, "/cameras/dashboard/") {
				data.BackURL = p
			} else if strings.HasPrefix(p, "/cameras/nvr/") {
				data.BackURL = p
			}
		}
	}
	if data.BackURL != "" {
		data.BackParam = "?back=" + url.QueryEscape(data.BackURL)
		// Extract a human-readable label from the URL
		if strings.HasPrefix(data.BackURL, "/cameras/dashboard/") {
			slug := strings.TrimPrefix(data.BackURL, "/cameras/dashboard/")
			slug = strings.TrimSuffix(slug, "/")
			data.BackLabel = strings.ReplaceAll(slug, "_", " ")
		} else {
			data.BackLabel = selectedNVR.Name
		}
	}
	data.DashboardType = models.GetDashboardTypeBySlug("cameras")
	data.DashboardTypes = models.DashboardTypes
	SetUserInfoFromRequest(r, &data.BaseDashboardData)

	renderTemplate(w, "camera_dashboard.html", data)
}

// CameraPartialHandler returns the camera content partial for HTMX refresh
func CameraPartialHandler(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	nvrID := r.URL.Query().Get("nvr")

	var data models.CameraDashboardData
	data.Go2RTCBaseURL = go2rtcProxyBaseURL
	data.Go2RTCDirectURL = go2rtcBaseURL
	data.UpdatedAt = time.Now()

	switch view {
	case "cameras":
		if nvrID != "" {
			nvr := findNVRByID(nvrID)
			if nvr != nil {
				data.CurrentNVR = nvr
				cameras := buildCameraList(nvr)
				if userID, filter := shouldFilterCameras(r); filter {
					cameras = filterCamerasByPermission(cameras, userID, nvr.ID)
				}
				data.Cameras = cameras
			}
		}
		data.View = "cameras"

	default:
		cameraNVRsMu.RLock()
		nvrs := make([]models.NVR, len(cameraNVRs))
		copy(nvrs, cameraNVRs)
		cameraNVRsMu.RUnlock()

		// Filter NVRs by permission
		if userID, filter := shouldFilterCameras(r); filter {
			nvrs = filterNVRsByPermission(nvrs, userID)
		}

		data.NVRs = nvrs
		data.View = "nvrs"
	}

	renderTemplate(w, "camera_content.html", data)
}

// CameraMetricsHandler returns camera data as JSON
func CameraMetricsHandler(w http.ResponseWriter, r *http.Request) {
	cameraNVRsMu.RLock()
	nvrs := make([]models.NVR, len(cameraNVRs))
	copy(nvrs, cameraNVRs)
	cameraNVRsMu.RUnlock()

	data := models.CameraDashboardData{
		View:          "nvrs",
		NVRs:          nvrs,
		Go2RTCBaseURL:   go2rtcProxyBaseURL,
		Go2RTCDirectURL: go2rtcBaseURL,
		UpdatedAt:     time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// CameraRenameHandler handles POST requests to rename a camera channel
func CameraRenameHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		NVRID   string `json:"nvrId"`
		Channel string `json:"channel"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.NVRID == "" || req.Channel == "" {
		http.Error(w, "nvrId and channel are required", http.StatusBadRequest)
		return
	}

	// Update in-memory overrides
	channelNameOverridesMu.Lock()
	if channelNameOverrides[req.NVRID] == nil {
		channelNameOverrides[req.NVRID] = make(map[string]string)
	}
	if req.Name == "" {
		// Empty name removes the override
		delete(channelNameOverrides[req.NVRID], req.Channel)
		if len(channelNameOverrides[req.NVRID]) == 0 {
			delete(channelNameOverrides, req.NVRID)
		}
	} else {
		channelNameOverrides[req.NVRID][req.Channel] = req.Name
	}
	channelNameOverridesMu.Unlock()

	// Persist to config file
	if err := saveChannelNameOverrides(); err != nil {
		log.Printf("Camera Dashboard: Failed to save channel name overrides: %v", err)
		http.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
