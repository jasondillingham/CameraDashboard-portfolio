package handlers

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"
)

// startHealthCheckLoop runs periodic health checks and logs metrics.
// Runs every 5 minutes to provide trend data without being noisy.
func startHealthCheckLoop() {
	// Wait for startup to finish before first check
	time.Sleep(30 * time.Second)
	logHealthCheck()

	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		logHealthCheck()
	}
}

func logHealthCheck() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Count playback sessions
	playbackSessionsMu.Lock()
	playbackCount := len(playbackSessions)
	var oldestIdle time.Duration
	for _, s := range playbackSessions {
		idle := time.Since(s.LastAccessed)
		if idle > oldestIdle {
			oldestIdle = idle
		}
	}
	playbackSessionsMu.Unlock()

	// Check go2rtc stream counts
	totalStreams := 0
	playbackStreams := 0
	go2rtcStatus := "ok"
	streams, err := cameraClient.GetStreams()
	if err != nil {
		go2rtcStatus = "unreachable: " + err.Error()
	} else {
		totalStreams = len(streams)
		for name := range streams {
			if strings.HasPrefix(name, "playback_") {
				playbackStreams++
			}
		}
	}

	// Check orphaned playback streams (in go2rtc but not in session map)
	orphanedStreams := 0
	if streams != nil {
		playbackSessionsMu.Lock()
		for name := range streams {
			if strings.HasPrefix(name, "playback_") {
				found := false
				for _, s := range playbackSessions {
					if s.StreamName == name {
						found = true
						break
					}
				}
				if !found {
					orphanedStreams++
				}
			}
		}
		playbackSessionsMu.Unlock()
	}

	// Check export temp directory size
	exportDirSize := int64(0)
	exportFileCount := 0
	exportDir := "/tmp/cameradashboard-exports"
	if entries, err := os.ReadDir(exportDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				exportDirSize += info.Size()
				exportFileCount++
			}
		}
	}

	// Check NVR online status
	cameraNVRsMu.RLock()
	nvrOnline := 0
	nvrOffline := 0
	var offlineNVRs []string
	for _, nvr := range cameraNVRs {
		if nvr.IsOnline {
			nvrOnline++
		} else if !nvr.UnderConstruction {
			nvrOffline++
			offlineNVRs = append(offlineNVRs, nvr.ID)
		}
	}
	cameraNVRsMu.RUnlock()

	// Log summary
	log.Printf("Health Check: go2rtc=%s streams=%d playback_streams=%d playback_sessions=%d orphaned=%d mem_alloc=%dMB mem_sys=%dMB goroutines=%d nvr_online=%d nvr_offline=%d exports=%d/%s",
		go2rtcStatus,
		totalStreams,
		playbackStreams,
		playbackCount,
		orphanedStreams,
		m.Alloc/1024/1024,
		m.Sys/1024/1024,
		runtime.NumGoroutine(),
		nvrOnline,
		nvrOffline,
		exportFileCount,
		formatBytes(exportDirSize),
	)

	// Warn on specific conditions
	if orphanedStreams > 0 {
		log.Printf("Health Check: WARNING - %d orphaned playback streams in go2rtc (not tracked in session map)", orphanedStreams)
	}
	if playbackStreams > 20 {
		log.Printf("Health Check: WARNING - %d playback streams in go2rtc, possible leak", playbackStreams)
	}
	if oldestIdle > 10*time.Minute {
		log.Printf("Health Check: WARNING - oldest playback session idle for %v (cleanup may be stuck)", oldestIdle)
	}
	if nvrOffline > 0 {
		log.Printf("Health Check: WARNING - %d NVRs offline: %s", nvrOffline, strings.Join(offlineNVRs, ", "))
	}
	if exportDirSize > 5*1024*1024*1024 { // 5GB
		log.Printf("Health Check: WARNING - export directory at %s (%d files)", formatBytes(exportDirSize), exportFileCount)
	}

	// Clean orphaned streams if found
	if orphanedStreams > 0 && streams != nil {
		go purgeOrphanedPlaybackStreams()
	}
}

func formatBytes(b int64) string {
	const mb = 1024 * 1024
	const gb = 1024 * mb
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1fGB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%dMB", b/mb)
	case b >= 1024:
		return fmt.Sprintf("%dKB", b/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
