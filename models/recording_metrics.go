package models

import (
	"os/exec"
	"time"
)

// RecordingSegment represents a single continuous recording on the NVR
type RecordingSegment struct {
	StartTime   time.Time `json:"startTime"`
	EndTime     time.Time `json:"endTime"`
	TrackID     string    `json:"trackID"`
	CodecType   string    `json:"codecType,omitempty"`
	PlaybackURI string    `json:"playbackURI,omitempty"`
}

// RecordingSearchResult holds the result of an ISAPI recording search
type RecordingSearchResult struct {
	NVRID    string             `json:"nvrId"`
	Channel  int                `json:"channel"`
	Date     string             `json:"date"` // YYYY-MM-DD
	Segments []RecordingSegment `json:"segments"`
}

// PlaybackSession tracks an active NVR playback stream
type PlaybackSession struct {
	ID           string    `json:"id"`
	StreamName   string    `json:"streamName"`
	NVRID        string    `json:"nvrId"`
	Channel      int       `json:"channel"`
	StartTime    string    `json:"startTime"`
	EndTime      string    `json:"endTime"`
	Quality      string    `json:"quality"` // "main" or "sub"
	Speed        float64   `json:"speed"`   // Playback speed (1, 2, 4, 8)
	CreatedAt    time.Time `json:"createdAt"`
	LastAccessed time.Time `json:"lastAccessed"`
}

// RecordingRequest holds fields shared by playback and export requests
type RecordingRequest struct {
	NVRID     string `json:"nvrId"`
	Channel   int    `json:"channel"`
	StartTime string `json:"startTime"` // ISO 8601
	EndTime   string `json:"endTime"`   // ISO 8601
	Quality   string `json:"quality"`   // "main" or "sub"
}

// PlaybackRequest is the JSON body for starting/seeking playback
type PlaybackRequest struct {
	RecordingRequest
	SessionID string  `json:"sessionId,omitempty"`
	Speed     float64 `json:"speed,omitempty"` // 1, 2, 4, 8 (default 1)
}

// ExportJob tracks a video export through the pipeline:
// queued → downloading → encoding → uploading → complete (or failed)
type ExportJob struct {
	ID          int64      `json:"id"`
	JobUID      string     `json:"jobUid"`
	NVRID       string     `json:"nvrId"`
	Channel     int        `json:"channel"`
	CameraName  string     `json:"cameraName"`
	NVRName     string     `json:"nvrName"`
	StartTime   string     `json:"startTime"`
	EndTime     string     `json:"endTime"`
	Quality     string     `json:"quality"`
	ExportName  string     `json:"exportName,omitempty"`
	Status      string     `json:"status"` // queued, downloading, encoding, uploading, complete, failed
	Progress    int        `json:"progress"`
	FilePath    string     `json:"-"`
	FileName    string     `json:"fileName,omitempty"`
	FileSize    int64      `json:"fileSize,omitempty"`
	Error       string     `json:"error,omitempty"`
	ShareLink   string     `json:"shareLink,omitempty"`
	RequestedBy string    `json:"requestedBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Cmd         *exec.Cmd  `json:"-"` // For cancellation of in-progress FFmpeg
}

// ExportRequest is the JSON body for starting an export
type ExportRequest struct {
	RecordingRequest
	CameraName string `json:"cameraName"`
	NVRName    string `json:"nvrName"`
	ExportName string `json:"exportName"`
}
