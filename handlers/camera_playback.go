package handlers

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cameradashboard/models"
	"cameradashboard/rtsp"

	"github.com/icholy/digest"
)

var (
	playbackSessions   = make(map[string]*models.PlaybackSession)
	playbackSessionsMu sync.Mutex
	maxSessionsPerNVR  = 4
	rtspRelay          *rtsp.Relay
)

// SetRTSPRelay sets the RTSP relay instance for playback speed control.
func SetRTSPRelay(r *rtsp.Relay) {
	rtspRelay = r
}

// ISAPI XML structures for recording search
type trackIDList struct {
	TrackID string `xml:"trackID"`
}

type timeSpanList struct {
	TimeSpan timeSpan `xml:"timeSpan"`
}

type timeSpan struct {
	StartTime string `xml:"startTime"`
	EndTime   string `xml:"endTime"`
}

type cmSearchResult struct {
	XMLName        xml.Name       `xml:"CMSearchResult"`
	ResponseStatus string         `xml:"responseStatus"`
	NumOfMatches   int            `xml:"numOfMatches"`
	MatchList      matchList      `xml:"matchList"`
}

type matchList struct {
	SearchMatchItems []searchMatchItem `xml:"searchMatchItem"`
}

type searchMatchItem struct {
	TrackID     string      `xml:"trackID"`
	TimeSpan    timeSpan    `xml:"timeSpan"`
	MediaSegmentDescriptor mediaSegmentDescriptor `xml:"mediaSegmentDescriptor"`
}

type mediaSegmentDescriptor struct {
	ContentType string `xml:"contentType"`
	CodecType   string `xml:"codecType"`
	PlaybackURI string `xml:"playbackURI"`
}

// searchNVRRecordings queries the NVR ISAPI for recordings on a given date.
// Hikvision NVRs cap results at ~64 per query, so we split the day into
// 4-hour windows and merge results to get full coverage.
func searchNVRRecordings(nvr *models.NVR, channel int, date string) (*models.RecordingSearchResult, error) {
	user, pass := getNVRCredentials(*nvr)
	if user == "" {
		return nil, fmt.Errorf("no credentials for NVR %s", nvr.ID)
	}

	trackID := fmt.Sprintf("%d01", channel)

	log.Printf("Camera Playback: Searching NVR %s channel %d for %s", nvr.ID, channel, date)

	searchURL := fmt.Sprintf("http://%s/ISAPI/ContentMgmt/search", nvr.IP)

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &digest.Transport{
			Username: user,
			Password: pass,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	result := &models.RecordingSearchResult{
		NVRID:   nvr.ID,
		Channel: channel,
		Date:    date,
	}

	// Split day into 4-hour windows to work around the ~64 result cap per query
	windows := [][2]string{
		{date + "T00:00:00Z", date + "T03:59:59Z"},
		{date + "T04:00:00Z", date + "T07:59:59Z"},
		{date + "T08:00:00Z", date + "T11:59:59Z"},
		{date + "T12:00:00Z", date + "T15:59:59Z"},
		{date + "T16:00:00Z", date + "T19:59:59Z"},
		{date + "T20:00:00Z", date + "T23:59:59Z"},
	}

	seen := make(map[string]bool) // deduplicate by startTime+endTime

	for _, w := range windows {
		segments, err := searchNVRTimeWindow(client, searchURL, trackID, w[0], w[1])
		if err != nil {
			log.Printf("Camera Playback: Warning: window %s-%s failed: %v", w[0], w[1], err)
			continue
		}
		for _, seg := range segments {
			key := seg.StartTime.Format(time.RFC3339) + "|" + seg.EndTime.Format(time.RFC3339)
			if !seen[key] {
				seen[key] = true
				result.Segments = append(result.Segments, seg)
			}
		}
	}

	// Warn if NVR is recording in H.265 — browsers can't decode it natively
	h265Logged := false
	for _, seg := range result.Segments {
		if seg.CodecType != "" && seg.CodecType != "H.264-BP" && seg.CodecType != "H.264" && !h265Logged {
			log.Printf("Camera Playback: WARNING - NVR %s channel %d is recording in %s (not H.264). Playback may fail in browsers without transcoding.", nvr.ID, channel, seg.CodecType)
			h265Logged = true
		}
	}

	log.Printf("Camera Playback: Found %d recording segments for NVR %s channel %d on %s", len(result.Segments), nvr.ID, channel, date)

	return result, nil
}

// searchNVRTimeWindow queries a single time window and returns all segments found.
func searchNVRTimeWindow(client *http.Client, searchURL, trackID, startTime, endTime string) ([]models.RecordingSegment, error) {
	searchUUID := make([]byte, 16)
	rand.Read(searchUUID)
	searchID := fmt.Sprintf("%08X-%04X-%04X-%04X-%012X",
		searchUUID[0:4], searchUUID[4:6], searchUUID[6:8], searchUUID[8:10], searchUUID[10:16])

	xmlBody := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<CMSearchDescription xmlns="http://www.isapi.org/ver20/XMLSchema">
<searchID>%s</searchID>
<trackIDList>
<trackID>%s</trackID>
</trackIDList>
<timeSpanList>
<timeSpan>
<startTime>%s</startTime>
<endTime>%s</endTime>
</timeSpan>
</timeSpanList>
<maxResults>500</maxResults>
<searchResultPosition>0</searchResultPosition>
<contentTypeList>
<contentType>video</contentType>
</contentTypeList>
</CMSearchDescription>`, searchID, trackID, startTime, endTime)

	resp, err := client.Post(searchURL, "text/xml", strings.NewReader(xmlBody))
	if err != nil {
		return nil, fmt.Errorf("ISAPI search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ISAPI search returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ISAPI response: %w", err)
	}

	var searchResult cmSearchResult
	if err := xml.Unmarshal(body, &searchResult); err != nil {
		return nil, fmt.Errorf("failed to parse ISAPI XML: %w", err)
	}

	var segments []models.RecordingSegment
	for _, item := range searchResult.MatchList.SearchMatchItems {
		start, err := time.Parse("2006-01-02T15:04:05Z", item.TimeSpan.StartTime)
		if err != nil {
			log.Printf("Camera Playback: WARNING - bad startTime %q in segment: %v", item.TimeSpan.StartTime, err)
			continue
		}
		end, err := time.Parse("2006-01-02T15:04:05Z", item.TimeSpan.EndTime)
		if err != nil {
			log.Printf("Camera Playback: WARNING - bad endTime %q in segment: %v", item.TimeSpan.EndTime, err)
			continue
		}

		segments = append(segments, models.RecordingSegment{
			StartTime:   start,
			EndTime:     end,
			TrackID:     item.TrackID,
			CodecType:   item.MediaSegmentDescriptor.CodecType,
			PlaybackURI: item.MediaSegmentDescriptor.PlaybackURI,
		})
	}

	log.Printf("Camera Playback: Window %s to %s returned %d segments", startTime, endTime, len(segments))

	return segments, nil
}

// generateSessionID creates a random session ID
func generateSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// createPlaybackSession creates a new playback session with go2rtc.
// When speed > 1, the stream is routed through the RTSP relay to inject Scale headers.
func createPlaybackSession(nvr *models.NVR, channel int, startTime, endTime, quality string, speed float64) (*models.PlaybackSession, error) {
	if speed <= 0 {
		speed = 1
	}
	playbackSessionsMu.Lock()
	defer playbackSessionsMu.Unlock()

	// Count sessions for this NVR
	nvrCount := 0
	var oldestSession *models.PlaybackSession
	for _, s := range playbackSessions {
		if s.NVRID == nvr.ID {
			nvrCount++
			if oldestSession == nil || s.LastAccessed.Before(oldestSession.LastAccessed) {
				oldestSession = s
			}
		}
	}

	// Evict LRU if at limit
	if nvrCount >= maxSessionsPerNVR && oldestSession != nil {
		log.Printf("Camera Playback: Evicting LRU session %s for NVR %s", oldestSession.ID, nvr.ID)
		cleanupPlaybackSessionLocked(oldestSession.ID)
	}

	// Hikvision NVRs only store main stream recordings - always use main for playback
	substream := false

	// Ensure NVR has credentials (DefaultNVRs don't store them directly)
	if nvr.Username == "" {
		user, pass := getNVRCredentials(*nvr)
		if user == "" {
			return nil, fmt.Errorf("no credentials available for NVR %s", nvr.ID)
		}
		nvr.Username = user
		nvr.Password = pass
	}

	// Format timestamps for RTSP URL (Hikvision format: YYYYMMDDTHHMMSSZ)
	rtspStart := formatISAPIToRTSP(startTime)
	rtspEnd := formatISAPIToRTSP(endTime)

	rtspURL := nvr.GetPlaybackRTSPURL(channel, substream, rtspStart, rtspEnd)

	// Log the URL (mask password)
	maskedURL := strings.Replace(rtspURL, nvr.Password, "***", 1)
	log.Printf("Camera Playback: RTSP URL: %s (speed=%.0fx)", maskedURL, speed)

	sessionID := generateSessionID()
	streamName := "playback_" + sessionID

	// Route through RTSP relay for speed control when speed > 1
	sourceURL := rtspURL
	if speed > 1 && rtspRelay != nil {
		rtspRelay.RegisterSession(sessionID, rtspURL, speed)
		sourceURL = rtspRelay.GetRelayURL(sessionID)
		log.Printf("Camera Playback: Using RTSP relay for %.0fx speed: %s", speed, sourceURL)
	}

	// Use per-NVR go2rtc client if this NVR has a local server
	client := nvrGo2RTCClient(nvr)

	if err := client.AddStream(streamName, sourceURL); err != nil {
		if speed > 1 && rtspRelay != nil {
			rtspRelay.UnregisterSession(sessionID)
		}
		return nil, fmt.Errorf("failed to add playback stream: %w", err)
	}

	session := &models.PlaybackSession{
		ID:           sessionID,
		StreamName:   streamName,
		NVRID:        nvr.ID,
		Channel:      channel,
		StartTime:    startTime,
		EndTime:      endTime,
		Quality:      quality,
		Speed:        speed,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	}

	playbackSessions[sessionID] = session

	// Verify stream was actually added to go2rtc
	if exists, err := client.StreamExists(streamName); err != nil {
		log.Printf("Camera Playback: WARNING - could not verify stream %s in go2rtc: %v", streamName, err)
	} else if !exists {
		log.Printf("Camera Playback: WARNING - stream %s not found in go2rtc after AddStream", streamName)
	}

	log.Printf("Camera Playback: Created session %s for NVR %s channel %d (%s to %s)",
		sessionID, nvr.ID, channel, startTime, endTime)

	return session, nil
}

// cleanupPlaybackSessionLocked removes a session (caller must hold lock)
func cleanupPlaybackSessionLocked(sessionID string) {
	session, ok := playbackSessions[sessionID]
	if !ok {
		return
	}

	// Use per-NVR go2rtc client if available
	client := cameraClient
	if nvr := findNVRByID(session.NVRID); nvr != nil {
		client = nvrGo2RTCClient(nvr)
	}
	deleted := false
	for attempt := 1; attempt <= 3; attempt++ {
		if err := client.DeleteStream(session.StreamName); err != nil {
			log.Printf("Camera Playback: Error deleting stream %s (attempt %d/3): %v", session.StreamName, attempt, err)
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
		} else {
			log.Printf("Camera Playback: Deleted stream %s for session %s", session.StreamName, sessionID)
			deleted = true
			break
		}
	}
	if !deleted {
		log.Printf("Camera Playback: WARNING - failed to delete stream %s after 3 attempts, will be caught by orphan cleanup", session.StreamName)
	}

	if rtspRelay != nil {
		rtspRelay.UnregisterSession(sessionID)
	}

	delete(playbackSessions, sessionID)
}

// cleanupPlaybackSession removes a session with locking
func cleanupPlaybackSession(sessionID string) {
	playbackSessionsMu.Lock()
	defer playbackSessionsMu.Unlock()
	cleanupPlaybackSessionLocked(sessionID)
}

// startPlaybackCleanupLoop periodically evicts stale playback sessions
func startPlaybackCleanupLoop() {
	// On startup, purge any orphaned playback streams left in go2rtc
	// (e.g. from a server restart where in-memory sessions were lost)
	go purgeOrphanedPlaybackStreams()

	ticker := time.NewTicker(60 * time.Second)
	for range ticker.C {
		playbackSessionsMu.Lock()
		now := time.Now()
		for id, session := range playbackSessions {
			if now.Sub(session.LastAccessed) > 5*time.Minute {
				log.Printf("Camera Playback: Evicting stale session %s (idle %v)",
					id, now.Sub(session.LastAccessed))
				cleanupPlaybackSessionLocked(id)
			}
		}
		playbackSessionsMu.Unlock()
	}
}

// purgeOrphanedPlaybackStreams removes any playback_* streams from go2rtc
// that are not tracked in the in-memory session map. This handles cleanup
// after server restarts where session state is lost but go2rtc retains streams.
func purgeOrphanedPlaybackStreams() {
	streams, err := cameraClient.GetStreams()
	if err != nil {
		log.Printf("Camera Playback: Could not check for orphaned streams: %v", err)
		return
	}

	playbackSessionsMu.Lock()
	tracked := make(map[string]bool)
	for _, s := range playbackSessions {
		tracked[s.StreamName] = true
	}
	playbackSessionsMu.Unlock()

	purged := 0
	for name := range streams {
		if len(name) > 9 && name[:9] == "playback_" && !tracked[name] {
			if err := cameraClient.DeleteStream(name); err != nil {
				log.Printf("Camera Playback: Failed to purge orphan %s: %v", name, err)
			} else {
				purged++
			}
		}
	}
	if purged > 0 {
		log.Printf("Camera Playback: Purged %d orphaned playback streams from go2rtc", purged)
	}
}

// formatISAPIToRTSP converts ISO 8601 (2026-01-28T10:00:00Z) to Hikvision RTSP format (20260128T100000Z)
func formatISAPIToRTSP(isoTime string) string {
	// Try multiple formats - JavaScript sends .000Z milliseconds
	formats := []string{
		time.RFC3339Nano,                // 2006-01-02T15:04:05.999999999Z07:00
		time.RFC3339,                    // 2006-01-02T15:04:05Z07:00
		"2006-01-02T15:04:05.000Z",     // JS toISOString()
		"2006-01-02T15:04:05Z",         // Without timezone offset
	}
	for _, f := range formats {
		if t, err := time.Parse(f, isoTime); err == nil {
			return t.UTC().Format("20060102T150405Z")
		}
	}
	log.Printf("Camera Playback: WARNING - could not parse timestamp: %s", isoTime)
	return isoTime
}

// CameraRecordingSearchHandler returns recording segments as JSON
func CameraRecordingSearchHandler(w http.ResponseWriter, r *http.Request) {
	nvrID := r.URL.Query().Get("nvr")
	channelStr := r.URL.Query().Get("channel")
	date := r.URL.Query().Get("date")

	if nvrID == "" || channelStr == "" || date == "" {
		http.Error(w, "Missing nvr, channel, or date parameter", http.StatusBadRequest)
		return
	}

	// Parse channel
	channel := 0
	for _, c := range channelStr {
		if c >= '0' && c <= '9' {
			channel = channel*10 + int(c-'0')
		}
	}

	// Find NVR
	nvr := findNVRByID(nvrID)
	if nvr == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	result, err := searchNVRRecordings(nvr, channel, date)
	if err != nil {
		log.Printf("Camera Playback: Search error: %v", err)
		http.Error(w, "Failed to search recordings", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("Camera Playback: Error encoding response: %v", err)
	}
}

// CameraPlaybackStartHandler creates a new playback session
func CameraPlaybackStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.PlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Find NVR
	nvr := findNVRByID(req.NVRID)
	if nvr == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	if req.Quality == "" {
		req.Quality = "sub"
	}

	session, err := createPlaybackSession(nvr, req.Channel, req.StartTime, req.EndTime, req.Quality, req.Speed)
	if err != nil {
		log.Printf("Camera Playback: Failed to create session: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(session); err != nil {
		log.Printf("Camera Playback: Error encoding session response: %v", err)
	}
}

// CameraPlaybackStopHandler stops an active playback session
func CameraPlaybackStopHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		// Try reading from body
		var body struct {
			SessionID string `json:"sessionId"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sessionID = body.SessionID
	}

	if sessionID == "" {
		http.Error(w, "Missing session parameter", http.StatusBadRequest)
		return
	}

	cleanupPlaybackSession(sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"stopped"}`))
}

// CameraPlaybackSeekHandler stops the old session and starts a new one
func CameraPlaybackSeekHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.PlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Stop old session if provided
	if req.SessionID != "" {
		cleanupPlaybackSession(req.SessionID)
	}

	// Find NVR
	nvr := findNVRByID(req.NVRID)
	if nvr == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	if req.Quality == "" {
		req.Quality = "sub"
	}

	session, err := createPlaybackSession(nvr, req.Channel, req.StartTime, req.EndTime, req.Quality, req.Speed)
	if err != nil {
		log.Printf("Camera Playback: Seek failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(session); err != nil {
		log.Printf("Camera Playback: Error encoding session response: %v", err)
	}
}

// CameraPlaybackViewHandler serves the playback page for a camera
func CameraPlaybackViewHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cameras/playback/")
	streamID := strings.TrimSuffix(path, "/")

	// Parse streamID to get NVR and channel
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

	camera := &models.Camera{
		ID:      selectedNVR.GetStreamName(channel),
		Name:    getChannelName(selectedNVR.ID, channel),
		Channel: channel,
		NVRID:   selectedNVR.ID,
		NVRName: selectedNVR.Name,
	}

	// Default date to today
	selectedDate := r.URL.Query().Get("date")
	if selectedDate == "" {
		selectedDate = time.Now().Format("2006-01-02")
	}

	mainStreamName := selectedNVR.GetStreamName(channel)
	subStreamName := selectedNVR.GetSubStreamName(channel)

	data := models.CameraDashboardData{
		View:            "playback",
		CurrentNVR:      selectedNVR,
		CurrentCamera:   camera,
		StreamName:      subStreamName,
		MainStreamName:  mainStreamName,
		SubStreamName:   subStreamName,
		Go2RTCBaseURL:   selectedNVR.Go2RTCProxyPath(),
		Go2RTCDirectURL: nvrGo2RTCURL(selectedNVR),
		UpdatedAt:       time.Now(),
		RefreshInterval: 0,
		PlaybackMode:    true,
		SelectedDate:    selectedDate,
	}
	data.DashboardType = models.GetDashboardTypeBySlug("cameras")
	data.DashboardTypes = models.DashboardTypes
	SetUserInfoFromRequest(r, &data.BaseDashboardData)

	renderTemplate(w, "camera_dashboard.html", data)
}
