package handlers

import (
	"log"
	"net/http"
	"sync"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

const (
	sessionActiveSeconds = 120 // Consider sessions active if heartbeat within 2 minutes
	sessionCleanupAge    = 300 // Remove sessions older than 5 minutes
)

var (
	cleanupOnce sync.Once
)

// InitAdminDashboard initializes the admin dashboard system
func InitAdminDashboard() {
	// Start background cleanup of stale sessions
	cleanupOnce.Do(func() {
		go sessionCleanupWorker()
	})
}

// sessionCleanupWorker periodically removes stale sessions
func sessionCleanupWorker() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if deleted, err := db.CleanupStaleSessions(sessionCleanupAge); err != nil {
			log.Printf("Error cleaning up stale sessions: %v", err)
		} else if deleted > 0 {
			log.Printf("Cleaned up %d stale sessions", deleted)
		}
	}
}

// AdminDashboardHandler serves the admin dashboard page
func AdminDashboardHandler(w http.ResponseWriter, r *http.Request) {
	user := contextGetAuthenticatedUser(r)

	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "sessions"
	}

	data := &models.AdminDashboardData{
		Tab:         tab,
		LockoutMode: IsLockoutMode(),
		UpdatedAt:   time.Now(),
		IsAdmin:     true,
	}

	if user != nil {
		data.User = &models.UserInfo{
			Username:    user.SamAccountName,
			DisplayName: user.DisplayName,
		}
	}
	data.CSRFToken = getOrCreateCSRFToken(r)
	log.Printf("[DEBUG] Admin page CSRFToken set: empty=%v, len=%d", data.CSRFToken == "", len(data.CSRFToken))

	// Get active sessions
	sessions, err := db.GetActiveSessions(sessionActiveSeconds)
	if err != nil {
		log.Printf("Error getting active sessions: %v", err)
	}
	data.Sessions = sessions

	// Get stats
	stats, err := db.GetAccessStats()
	if err != nil {
		log.Printf("Error getting access stats: %v", err)
	}
	data.Stats = stats

	data.SetDashboardInfo("cameras", db.GetDatabaseInfo())

	renderTemplate(w, "admin_dashboard.html", data)
}

// AdminKillSwitchHandler toggles lockout mode.
// When enabling: purges all sessions, restarts go2rtc, blocks non-admin login.
// When disabling: lifts lockout so users can log back in.
func AdminKillSwitchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := contextGetAuthenticatedUser(r)
	action := r.FormValue("action") // "enable" or "disable"

	if action == "disable" {
		SetLockoutMode(false)
		log.Printf("[ADMIN] Kill switch DISABLED by %s — users may log in again", user.SamAccountName)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","lockout":false}`))
		return
	}

	// --- Enable lockout ---
	SetLockoutMode(true)
	log.Printf("[ADMIN] Kill switch ENABLED by %s — purging sessions, restarting go2rtc", user.SamAccountName)

	// Purge all sessions (logs out everyone including this admin, but their
	// next request will just re-prompt login — admins can still log in).
	if err := db.PurgeAllSessions(); err != nil {
		log.Printf("[ADMIN] Failed to purge sessions: %v", err)
	}

	// Restart go2rtc (drops all WebSocket/RTSP connections)
	if err := cameraClient.Restart(); err != nil {
		log.Printf("[ADMIN] go2rtc restart request failed: %v", err)
	}

	// Restart per-NVR go2rtc instances (remote branch servers)
	for nvrID, client := range nvrGo2RTCClients {
		if err := client.Restart(); err != nil {
			log.Printf("[ADMIN] go2rtc restart for NVR %s failed: %v", nvrID, err)
		} else {
			log.Printf("[ADMIN] go2rtc restarted for NVR %s", nvrID)
		}
	}

	// Clear snapshot cache
	snapshotCacheMu.Lock()
	snapshotCache = nil
	snapshotCacheMu.Unlock()

	// Background: wait for go2rtc to recover, then re-register streams
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			if err := cameraClient.HealthCheck(); err == nil {
				log.Printf("[ADMIN] go2rtc back online after %d seconds, re-registering streams", i+1)

				cameraNVRsMu.RLock()
				nvrs := make([]models.NVR, len(cameraNVRs))
				copy(nvrs, cameraNVRs)
				cameraNVRsMu.RUnlock()

				existingStreams, _ := cameraClient.GetStreams()
				for i := range nvrs {
					registerNVRStreams(&nvrs[i], existingStreams)
				}
				log.Printf("[ADMIN] Stream re-registration complete")
				return
			}
		}
		log.Printf("[ADMIN] WARNING: go2rtc did not come back within 10 seconds")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","lockout":true}`))
}

// AdminStatsHandler serves the statistics tab content
func AdminStatsHandler(w http.ResponseWriter, r *http.Request) {
	stats, err := db.GetAccessStats()
	if err != nil {
		log.Printf("Error getting access stats: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	data := &models.AdminDashboardData{
		Tab:       "stats",
		Stats:     stats,
		UpdatedAt: time.Now(),
	}

	renderTemplate(w, "admin_stats_content.html", data)
}

// AdminUniqueUsersHandler serves the unique users detail page
func AdminUniqueUsersHandler(w http.ResponseWriter, r *http.Request) {
	users, err := db.GetTodayUniqueUsers()
	if err != nil {
		log.Printf("Error getting unique users: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	data := struct {
		Users     []models.UniqueUserDetail
		UpdatedAt time.Time
	}{
		Users:     users,
		UpdatedAt: time.Now(),
	}

	renderTemplate(w, "admin_unique_users_content.html", data)
}
