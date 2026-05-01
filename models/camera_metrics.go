package models

import (
	"strconv"
	"time"
)

// NVRConfig holds the configuration for all NVRs loaded from config file
type NVRConfig struct {
	NVRs []NVR `json:"nvrs"`
}

// NVR represents a Network Video Recorder
type NVR struct {
	ID       string   `json:"id"`       // Unique identifier (e.g., "hq_sales")
	Name     string   `json:"name"`     // Display name (e.g., "HQ Sales")
	IP       string   `json:"ip"`       // IP address
	Port     int      `json:"port"`     // RTSP port (default 554)
	Username string   `json:"username"` // RTSP username
	Password string   `json:"password"` // RTSP password
	Channels int      `json:"channels"` // Number of camera channels
	Cameras  []Camera `json:"cameras"`  // Optional camera definitions
	Go2RTCURL         string `json:"go2rtcUrl,omitempty"` // Per-NVR go2rtc URL (if local server)
	IsOnline          bool `json:"isOnline"`          // Runtime status
	UnderConstruction bool `json:"underConstruction"` // Show under construction badge
}

// Camera represents a single camera on an NVR
type Camera struct {
	ID          string `json:"id"`          // Unique identifier (e.g., "hq_sales_cam1")
	Name        string `json:"name"`        // Display name (e.g., "Front Entrance")
	Channel     int    `json:"channel"`     // Channel number on NVR (1-based)
	StreamURL   string `json:"streamURL"`   // Full RTSP URL (computed at runtime)
	NVRID       string `json:"nvrId"`       // Parent NVR ID
	NVRName     string `json:"nvrName"`     // Parent NVR name (for display)
	Go2RTCProxy string `json:"go2rtcProxy,omitempty"` // Per-camera go2rtc proxy path (for grid view)
}

// CameraDashboardPreset defines a multi-camera grid dashboard preset
type CameraDashboardPreset struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	GridSize string                 `json:"gridSize"` // "2x2", "3x3", "4x4"
	Cameras  []CameraDashboardEntry `json:"cameras"`
}

// CameraDashboardEntry maps a camera in a dashboard preset to an NVR channel
type CameraDashboardEntry struct {
	NVRID   string `json:"nvrId"`
	Channel int    `json:"channel"`
}

// CameraDashboardData holds all data for the camera dashboard
type CameraDashboardData struct {
	BaseDashboardData
	View            string    `json:"view"`            // "nvrs", "cameras", "live", or "grid"
	NVRs            []NVR     `json:"nvrs"`            // All configured NVRs
	CurrentNVR      *NVR      `json:"currentNVR"`      // Selected NVR (for camera list)
	Cameras         []Camera  `json:"cameras"`         // Cameras for selected NVR
	CurrentCamera   *Camera   `json:"currentCamera"`   // Selected camera (for live view)
	StreamName      string    `json:"streamName"`      // go2rtc stream name (active)
	MainStreamName  string    `json:"mainStreamName"`  // Main stream name for toggle
	SubStreamName   string    `json:"subStreamName"`   // Sub stream name for toggle
	Go2RTCBaseURL   string    `json:"go2rtcBaseURL"`   // Base URL for go2rtc (proxy path for JS)
	Go2RTCDirectURL string    `json:"go2rtcDirectURL"` // Direct go2rtc URL for WebSocket
	UpdatedAt       time.Time `json:"updatedAt"`       // Last refresh time
	RefreshInterval int       `json:"refreshInterval"` // Refresh interval in seconds
	Error           string    `json:"error,omitempty"` // Error message if any

	// Playback fields
	PlaybackMode    bool                   `json:"playbackMode,omitempty"`
	PlaybackSession *PlaybackSession       `json:"playbackSession,omitempty"`
	Recordings      *RecordingSearchResult  `json:"recordings,omitempty"`
	SelectedDate    string                 `json:"selectedDate,omitempty"`

	// Grid dashboard fields
	DashboardPresets []CameraDashboardPreset `json:"dashboardPresets,omitempty"`
	CurrentPreset    *CameraDashboardPreset  `json:"currentPreset,omitempty"`
	GridCameras      []Camera               `json:"gridCameras,omitempty"`
	BackURL          string                 `json:"backURL,omitempty"`
	BackParam        string                 `json:"-"` // URL-encoded "?back=..." for embedding in links
	BackLabel        string                 `json:"backLabel,omitempty"`

	// Pagination fields (for NVR camera list)
	CurrentPage int `json:"currentPage,omitempty"`
	TotalPages  int `json:"totalPages,omitempty"`
	PageSize    int `json:"pageSize,omitempty"`
}

// rtspBase builds the common RTSP URL prefix: rtsp://user:pass@ip:port
func (nvr *NVR) rtspBase() string {
	port := nvr.Port
	if port == 0 {
		port = 554
	}
	return "rtsp://" + nvr.Username + ":" + nvr.Password + "@" + nvr.IP + ":" + strconv.Itoa(port)
}

// streamSuffix returns "01" for main stream, "02" for substream
func streamSuffix(substream bool) string {
	if substream {
		return "02"
	}
	return "01"
}

// GetRTSPURL builds the RTSP URL for a camera channel on an NVR
// Hikvision format: rtsp://user:pass@ip:port/Streaming/Channels/X01
// where X is the channel number and 01 is main stream (02 is substream)
func (nvr *NVR) GetRTSPURL(channel int, substream bool) string {
	return nvr.rtspBase() + "/Streaming/Channels/" + strconv.Itoa(channel) + streamSuffix(substream)
}

// GetPlaybackRTSPURL builds the RTSP playback URL for recording retrieval
// Hikvision format: rtsp://user:pass@ip:port/Streaming/tracks/{channel}{01|02}?starttime=X&endtime=Y
// startTime/endTime format: 20260128T100000Z
func (nvr *NVR) GetPlaybackRTSPURL(channel int, substream bool, startTime, endTime string) string {
	return nvr.rtspBase() + "/Streaming/tracks/" + strconv.Itoa(channel) + streamSuffix(substream) +
		"/?starttime=" + startTime + "&endtime=" + endTime
}

// GetStreamName returns a sanitized stream name for go2rtc (main stream)
func (nvr *NVR) GetStreamName(channel int) string {
	return nvr.ID + "_cam" + strconv.Itoa(channel)
}

// GetSubStreamName returns the sub stream name for go2rtc
func (nvr *NVR) GetSubStreamName(channel int) string {
	return nvr.ID + "_cam" + strconv.Itoa(channel) + "_sub"
}

// Go2RTCProxyPath returns the proxy base URL for this NVR's go2rtc.
// NVRs with a local go2rtc use /go2rtc-nvr/{id}, others use the default /go2rtc.
func (nvr *NVR) Go2RTCProxyPath() string {
	if nvr.Go2RTCURL != "" {
		return "/go2rtc-nvr/" + nvr.ID
	}
	return "/go2rtc"
}

// DefaultNVRs returns a fallback NVR list when camera_config.json is unavailable.
// Full NVR inventory is defined in ../configs/camera_config.json.
func DefaultNVRs() []NVR {
	return []NVR{
		{ID: "hq_office", Name: "HQ Office", IP: "10.0.10.3", Port: 554, Channels: 16},
	}
}
