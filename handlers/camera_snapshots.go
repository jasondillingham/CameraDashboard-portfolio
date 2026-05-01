package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// snapshotCache stores JPEG snapshots keyed by stream name
	snapshotCache   map[string]*snapshotEntry
	snapshotCacheMu sync.RWMutex
)

type snapshotEntry struct {
	data      []byte
	fetchedAt time.Time
}

const snapshotCacheTTL = 1 * time.Hour

// CameraSnapshotHandler serves a cached JPEG snapshot for a camera stream
func CameraSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	streamName := strings.TrimPrefix(r.URL.Path, "/cameras/snapshot/")
	streamName = strings.TrimSuffix(streamName, "/")

	if streamName == "" {
		http.Error(w, "Missing stream name", http.StatusBadRequest)
		return
	}

	// Check camera-level permission
	if nvrID, channel, err := parseStreamID(streamName); err == nil {
		if !checkCameraAccess(w, r, nvrID, channel) {
			return
		}
	}

	// Check cache first
	snapshotCacheMu.RLock()
	if snapshotCache != nil {
		if entry, ok := snapshotCache[streamName]; ok && time.Since(entry.fetchedAt) < snapshotCacheTTL {
			snapshotCacheMu.RUnlock()
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Write(entry.data)
			return
		}
	}
	snapshotCacheMu.RUnlock()

	// Fetch from go2rtc using sub-stream for smaller/faster thumbnails
	subStream := streamName
	if !strings.HasSuffix(subStream, "_sub") {
		subStream = streamName + "_sub"
	}

	// Use per-NVR go2rtc client if available
	client := cameraClient
	if nvrID, _, err := parseStreamID(streamName); err == nil {
		if nvr := findNVRByID(nvrID); nvr != nil {
			client = nvrGo2RTCClient(nvr)
		}
	}

	data, err := client.GetFrameJPEG(subStream)
	if err != nil {
		// Fallback to main stream
		data, err = client.GetFrameJPEG(streamName)
		if err != nil {
			http.Error(w, "Failed to capture frame", http.StatusBadGateway)
			return
		}
	}

	// Cache it
	snapshotCacheMu.Lock()
	if snapshotCache == nil {
		snapshotCache = make(map[string]*snapshotEntry)
	}
	snapshotCache[streamName] = &snapshotEntry{data: data, fetchedAt: time.Now()}
	snapshotCacheMu.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
}

// CameraSnapshotClearHandler clears the snapshot cache (for refresh button)
func CameraSnapshotClearHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshotCacheMu.Lock()
	snapshotCache = nil
	snapshotCacheMu.Unlock()

	log.Printf("Camera Dashboard: Snapshot cache cleared by admin")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
