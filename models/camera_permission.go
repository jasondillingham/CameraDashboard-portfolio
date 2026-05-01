package models

// CameraPermission represents a per-NVR/camera permission grant
// Channel=0 means all cameras on the NVR
type CameraPermission struct {
	ID      int64  `json:"id"`
	UserID  string `json:"userId"`
	NVRID   string `json:"nvrId"`
	Channel int    `json:"channel"` // 0 = all cameras
}

// CameraPresetPermission represents access to a grid dashboard preset
type CameraPresetPermission struct {
	ID       int64  `json:"id"`
	UserID   string `json:"userId"`
	PresetID string `json:"presetId"`
}

// CameraPermissionsPageData holds data for the admin camera permissions panel
type CameraPermissionsPageData struct {
	UserID      string                 `json:"userId"`
	DisplayName string                 `json:"displayName"`
	NVRs        []CameraPermNVR        `json:"nvrs"`
	Presets     []CameraPermPreset     `json:"presets"`
	Tab         string                 `json:"tab"`
}

// CameraPermNVR holds NVR info with per-channel grant status for the admin UI
type CameraPermNVR struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Channels int               `json:"channels"`
	AllGrant bool              `json:"allGrant"` // channel=0 grant exists
	Grants   map[int]bool      `json:"grants"`   // channel -> granted (1-based)
	CameraNames map[int]string `json:"cameraNames"` // channel -> display name
}

// CameraPermPreset holds preset info with grant status for the admin UI
type CameraPermPreset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Granted bool   `json:"granted"`
}
