package handlers

import (
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cameradashboard/go2rtc"
	"cameradashboard/models"

	"github.com/icholy/digest"
)

var (
	cameraClient  *go2rtc.Client
	cameraNVRs    []models.NVR
	cameraNVRsMu  sync.RWMutex
	cameraConfig  *CameraConfig
	go2rtcBaseURL      = "http://localhost:1984" // default go2rtc URL
	go2rtcProxyBaseURL = "/go2rtc"               // same-origin proxy path for templates

	// channelNames caches channel names per NVR ID (from ISAPI)
	channelNames   map[string]map[int]string // nvrID -> channel -> name
	channelNamesMu sync.RWMutex

	// channelNameOverrides stores user-set custom names (from config file)
	channelNameOverrides   map[string]map[string]string // nvrID -> channel (string) -> name
	channelNameOverridesMu sync.RWMutex

	// cameraDashboardPresets stores grid dashboard presets from config
	cameraDashboardPresets []models.CameraDashboardPreset

	// nvrGo2RTCClients maps NVR IDs to per-NVR go2rtc clients (for NVRs with local go2rtc)
	nvrGo2RTCClients map[string]*go2rtc.Client
)

// sanitizeURL strips credentials from a URL for safe logging.
func sanitizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

// nvrGo2RTCURL returns the direct go2rtc URL for an NVR, falling back to the default.
func nvrGo2RTCURL(nvr *models.NVR) string {
	if nvr.Go2RTCURL != "" {
		return nvr.Go2RTCURL
	}
	return go2rtcBaseURL
}

// nvrGo2RTCClient returns the go2rtc client for an NVR, falling back to the default.
func nvrGo2RTCClient(nvr *models.NVR) *go2rtc.Client {
	if nvr.Go2RTCURL != "" {
		if client, ok := nvrGo2RTCClients[nvr.ID]; ok {
			return client
		}
	}
	return cameraClient
}

// CameraConfig holds camera dashboard configuration
type CameraConfig struct {
	Go2RTCURL    string                        `json:"go2rtcUrl"`
	NVRs         []models.NVR                  `json:"nvrs"`
	Dashboards   []models.CameraDashboardPreset `json:"dashboards"`
	ChannelNames map[string]map[string]string   `json:"channelNames,omitempty"`
}

// ISAPI XML structures for parsing channel names
type inputProxyChannelList struct {
	Channels []inputProxyChannel `xml:"InputProxyChannel"`
}

type inputProxyChannel struct {
	ID   int    `xml:"id"`
	Name string `xml:"name"`
}

// InitCameraDashboard initializes the camera dashboard
func InitCameraDashboard() {
	channelNames = make(map[string]map[int]string)

	// Try to load config from file
	configPaths := []string{
		os.Getenv("CAMERA_CONFIG_PATH"),
		"./config/camera_config.json",
		"../configs/camera_config.json",
	}

	var config CameraConfig
	loaded := false

	for _, path := range configPaths {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(data, &config); err == nil {
				loaded = true
				log.Printf("Camera Dashboard: Loaded config from %s", path)
				break
			}
		}
	}

	if !loaded {
		log.Printf("Camera Dashboard: Using default configuration")
		config.Go2RTCURL = go2rtcBaseURL
		config.NVRs = models.DefaultNVRs()
	}

	if config.Go2RTCURL != "" {
		go2rtcBaseURL = config.Go2RTCURL
	}

	cameraConfig = &config
	cameraDashboardPresets = config.Dashboards

	// Load channel name overrides from config
	channelNameOverrides = make(map[string]map[string]string)
	if config.ChannelNames != nil {
		for nvrID, names := range config.ChannelNames {
			channelNameOverrides[nvrID] = make(map[string]string)
			for ch, name := range names {
				channelNameOverrides[nvrID][ch] = name
			}
		}
		log.Printf("Camera Dashboard: Loaded channel name overrides for %d NVRs", len(config.ChannelNames))
	}
	cameraClient = go2rtc.NewClient(go2rtcBaseURL)

	// Initialize per-NVR go2rtc clients for NVRs with local go2rtc servers
	nvrGo2RTCClients = make(map[string]*go2rtc.Client)
	for _, nvr := range config.NVRs {
		if nvr.Go2RTCURL != "" {
			nvrGo2RTCClients[nvr.ID] = go2rtc.NewClient(nvr.Go2RTCURL)
			log.Printf("Camera Dashboard: NVR %s using local go2rtc at %s", nvr.ID, sanitizeURL(nvr.Go2RTCURL))
		}
	}

	// Store NVRs
	cameraNVRsMu.Lock()
	cameraNVRs = config.NVRs
	if len(cameraNVRs) == 0 {
		cameraNVRs = models.DefaultNVRs()
	}
	cameraNVRsMu.Unlock()

	// Check go2rtc health, register streams, fetch channel names (non-blocking)
	go func() {
		// Retry health check — go2rtc may not be ready yet if both services
		// start simultaneously (systemd race condition)
		var connected bool
		for attempt := 1; attempt <= 5; attempt++ {
			if err := cameraClient.HealthCheck(); err != nil {
				log.Printf("Camera Dashboard: go2rtc not available (attempt %d/5): %v", attempt, err)
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
				continue
			}
			connected = true
			break
		}
		if !connected {
			log.Printf("Camera Dashboard: WARNING - go2rtc not available after 5 attempts, NVR online status will not be set")
			return
		}
		log.Printf("Camera Dashboard: go2rtc connected at %s", go2rtcBaseURL)

		// Fetch channel names and check NVR status
		cameraNVRsMu.RLock()
		nvrs := make([]models.NVR, len(cameraNVRs))
		copy(nvrs, cameraNVRs)
		cameraNVRsMu.RUnlock()

		// Get shared credentials from the first NVR that has go2rtc streams
		var sharedUser, sharedPass string
		for _, nvr := range nvrs {
			user, pass := getNVRCredentials(nvr)
			if user != "" {
				sharedUser, sharedPass = user, pass
				log.Printf("Camera Dashboard: Got shared credentials from NVR %s", nvr.ID)
				break
			}
		}

		// Apply shared credentials first, then fetch names and register streams
		for i := range nvrs {
			if nvrs[i].Username == "" && sharedUser != "" {
				nvrs[i].Username = sharedUser
				nvrs[i].Password = sharedPass
			}
		}

		// Fetch existing go2rtc streams once to avoid per-channel HTTP calls
		existingStreams, err := cameraClient.GetStreams()
		if err != nil {
			log.Printf("Camera Dashboard: Failed to fetch existing streams: %v", err)
			existingStreams = nil
		}

		for i := range nvrs {
			fetchChannelNames(nvrs[i])
			nvrs[i].IsOnline = checkNVROnline(nvrs[i])
			registerNVRStreams(&nvrs[i], existingStreams)
		}

		// Update stored NVRs with online status and credentials
		cameraNVRsMu.Lock()
		for i := range cameraNVRs {
			for _, nvr := range nvrs {
				if cameraNVRs[i].ID == nvr.ID {
					cameraNVRs[i].IsOnline = nvr.IsOnline
					cameraNVRs[i].Username = nvr.Username
					cameraNVRs[i].Password = nvr.Password
					break
				}
			}
		}
		cameraNVRsMu.Unlock()
	}()

	// Start playback cleanup loop (export cleanup is handled by export worker)
	initExportDir()
	go startPlaybackCleanupLoop()
	go startHealthCheckLoop()

	log.Printf("Camera Dashboard: Initialized with %d NVRs", len(cameraNVRs))
}

// findNVRByID returns a copy of the NVR with the given ID, or nil if not found.
func findNVRByID(id string) *models.NVR {
	cameraNVRsMu.RLock()
	defer cameraNVRsMu.RUnlock()
	for i := range cameraNVRs {
		if cameraNVRs[i].ID == id {
			nvr := cameraNVRs[i]
			return &nvr
		}
	}
	return nil
}

// fetchChannelNames queries the NVR ISAPI to get channel names
func fetchChannelNames(nvr models.NVR) {
	// Get credentials from go2rtc stream config
	user, pass := getNVRCredentials(nvr)
	if user == "" {
		log.Printf("Camera Dashboard: No credentials for NVR %s, using generic names", nvr.ID)
		return
	}

	url := fmt.Sprintf("http://%s/ISAPI/ContentMgmt/InputProxy/channels", nvr.IP)

	// Hikvision NVRs use digest authentication
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &digest.Transport{
			Username: user,
			Password: pass,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		log.Printf("Camera Dashboard: Failed to fetch channels from NVR %s: %v", nvr.ID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Camera Dashboard: NVR %s returned status %d", nvr.ID, resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Camera Dashboard: Failed to read NVR %s response: %v", nvr.ID, err)
		return
	}

	var channelList inputProxyChannelList
	if err := xml.Unmarshal(body, &channelList); err != nil {
		log.Printf("Camera Dashboard: Failed to parse NVR %s XML: %v", nvr.ID, err)
		return
	}

	names := make(map[int]string)
	for _, ch := range channelList.Channels {
		names[ch.ID] = ch.Name
	}

	channelNamesMu.Lock()
	channelNames[nvr.ID] = names
	channelNamesMu.Unlock()

	log.Printf("Camera Dashboard: Loaded %d channel names from NVR %s", len(names), nvr.ID)
}

// checkNVROnline checks if an NVR is reachable by testing its HTTP ISAPI endpoint
func checkNVROnline(nvr models.NVR) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/ISAPI/System/status", nvr.IP))
	if err != nil {
		return false
	}
	resp.Body.Close()
	// Any response (even 401 unauthorized) means the NVR is online
	return true
}

// getNVRCredentials extracts credentials from the first go2rtc stream for this NVR
func getNVRCredentials(nvr models.NVR) (string, string) {
	// Check if NVR has credentials directly
	if nvr.Username != "" {
		return nvr.Username, nvr.Password
	}

	// Try to get credentials from go2rtc streams
	streams, err := cameraClient.GetStreams()
	if err != nil {
		return "", ""
	}

	streamName := nvr.GetStreamName(1)
	stream, exists := streams[streamName]
	if !exists || len(stream.Producers) == 0 {
		return "", ""
	}

	// Parse credentials from RTSP URL: rtsp://user:pass@host:port/...
	src := stream.Producers[0].URL
	if !strings.Contains(src, "@") {
		return "", ""
	}

	authPart := strings.Split(strings.TrimPrefix(src, "rtsp://"), "@")[0]
	parts := strings.SplitN(authPart, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}

	return parts[0], parts[1]
}

// getChannelName returns the name for a channel with priority: user override > ISAPI > default
func getChannelName(nvrID string, channel int) string {
	// Check user overrides first
	channelNameOverridesMu.RLock()
	if names, ok := channelNameOverrides[nvrID]; ok {
		if name, ok := names[fmt.Sprintf("%d", channel)]; ok {
			channelNameOverridesMu.RUnlock()
			return name
		}
	}
	channelNameOverridesMu.RUnlock()

	// Then check ISAPI-fetched names
	channelNamesMu.RLock()
	defer channelNamesMu.RUnlock()

	if names, ok := channelNames[nvrID]; ok {
		if name, ok := names[channel]; ok {
			return name
		}
	}
	return "Camera " + strconv.Itoa(channel)
}

// buildCameraList creates a camera list for an NVR with real names
func buildCameraList(nvr *models.NVR) []models.Camera {
	cameras := make([]models.Camera, nvr.Channels)
	for i := 0; i < nvr.Channels; i++ {
		channel := i + 1
		cameras[i] = models.Camera{
			ID:        nvr.GetStreamName(channel),
			Name:      getChannelName(nvr.ID, channel),
			Channel:   channel,
			StreamURL: nvr.GetSubStreamName(channel),
			NVRID:     nvr.ID,
			NVRName:   nvr.Name,
		}
	}
	// Sort by channel number (already in order, but be explicit)
	sort.Slice(cameras, func(i, j int) bool {
		return cameras[i].Channel < cameras[j].Channel
	})
	return cameras
}

// registerNVRStreams registers all main and sub streams for an NVR with go2rtc.
// existingStreams is a pre-fetched snapshot of go2rtc streams to avoid per-channel HTTP calls.
func registerNVRStreams(nvr *models.NVR, existingStreams go2rtc.StreamsResponse) {
	// NVRs with their own local go2rtc have streams pre-configured in the local config
	if nvr.Go2RTCURL != "" {
		log.Printf("Camera Dashboard: Skipping stream registration for NVR %s (local go2rtc at %s)", nvr.ID, sanitizeURL(nvr.Go2RTCURL))
		return
	}

	if nvr.Username == "" {
		log.Printf("Camera Dashboard: Skipping stream registration for NVR %s (no credentials)", nvr.ID)
		return
	}

	registered := 0
	for ch := 1; ch <= nvr.Channels; ch++ {
		mainName := nvr.GetStreamName(ch)
		subName := nvr.GetSubStreamName(ch)
		mainURL := nvr.GetRTSPURL(ch, false)
		subURL := nvr.GetRTSPURL(ch, true)

		// Check if main stream already exists (in-memory map lookup)
		if _, exists := existingStreams[mainName]; !exists {
			// Add RTSP source with ffmpeg H.264 transcoding fallback for H.265 streams
			// go2rtc will use ffmpeg when WebRTC can't handle H.265 natively
			ffmpegSource := fmt.Sprintf("ffmpeg:%s#video=h264", mainName)
			if err := cameraClient.AddStreamWithSources(mainName, []string{mainURL, ffmpegSource}); err != nil {
				log.Printf("Camera Dashboard: Failed to register stream %s: %v", mainName, err)
				continue
			}
		}

		// Check if sub stream already exists
		if _, exists := existingStreams[subName]; !exists {
			// Add ffmpeg H.264 transcode fallback for sub streams too (H.265 breaks iPad MSE)
			ffmpegSubSource := fmt.Sprintf("ffmpeg:%s#video=h264", subName)
			if err := cameraClient.AddStreamWithSources(subName, []string{subURL, ffmpegSubSource}); err != nil {
				log.Printf("Camera Dashboard: Failed to register stream %s: %v", subName, err)
				continue
			}
		}

		registered++
	}

	log.Printf("Camera Dashboard: Registered %d/%d channel streams for NVR %s", registered, nvr.Channels, nvr.ID)
}

// saveChannelNameOverrides reads the config file, updates channelNames, and writes back
func saveChannelNameOverrides() error {
	configPath := os.Getenv("CAMERA_CONFIG_PATH")
	if configPath == "" {
		// Prefer app-local config dir (production), fall back to shared dev configs
		if _, err := os.Stat("./config/camera_config.json"); err == nil {
			configPath = "./config/camera_config.json"
		} else {
			configPath = "../configs/camera_config.json"
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Update the channelNames key with current overrides
	channelNameOverridesMu.RLock()
	overrides := make(map[string]map[string]string, len(channelNameOverrides))
	for nvrID, names := range channelNameOverrides {
		overrides[nvrID] = make(map[string]string, len(names))
		for ch, name := range names {
			overrides[nvrID][ch] = name
		}
	}
	channelNameOverridesMu.RUnlock()

	if len(overrides) > 0 {
		overridesJSON, err := json.Marshal(overrides)
		if err != nil {
			return fmt.Errorf("marshal overrides: %w", err)
		}
		raw["channelNames"] = overridesJSON
	} else {
		delete(raw, "channelNames")
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(configPath, out, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// GetCameraNVRs returns a copy of the configured NVRs (exported for admin UI)
func GetCameraNVRs() []models.NVR {
	cameraNVRsMu.RLock()
	defer cameraNVRsMu.RUnlock()
	nvrs := make([]models.NVR, len(cameraNVRs))
	copy(nvrs, cameraNVRs)
	return nvrs
}

// GetCameraPresets returns the configured dashboard presets (exported for admin UI)
func GetCameraPresets() []models.CameraDashboardPreset {
	return cameraDashboardPresets
}

// GetChannelNameExported exposes getChannelName for the admin UI
func GetChannelNameExported(nvrID string, channel int) string {
	return getChannelName(nvrID, channel)
}
