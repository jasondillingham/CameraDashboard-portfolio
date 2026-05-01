package handlers

import (
	"net/http"

	"github.com/alexedwards/scs/v2"
)

// Route defines a single route with its handler and protection level
type Route struct {
	Pattern   string
	Handler   http.HandlerFunc
	Protected bool // true = requires auth, false = public
}

// AdminRoute defines a route that requires admin privileges
type AdminRoute struct {
	Pattern string
	Handler http.HandlerFunc
}

// RegisterRoutes registers all application routes with the given session manager
func RegisterRoutes(sm *scs.SessionManager) {
	// Serve static files (CSS, JS) with cache headers — no auth required
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	http.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		staticFS.ServeHTTP(w, r)
	}))

	// Public routes (no auth required)
	publicRoutes := []Route{
		{"/api/version", VersionHandler, false},
		{"/login", LoginHandler, false},
	}

	// Protected routes (auth required)
	protectedRoutes := []Route{
		// Auth
		{"/logout", LogoutHandler, true},

		// Landing
		{"/", LandingHandler, true},

		// Camera Dashboard
		{"/cameras/snapshot/clear", CameraSnapshotClearHandler, true},
		{"/cameras/snapshot/", CameraSnapshotHandler, true},
		{"/cameras/rename", CameraRenameHandler, true},
		{"/cameras/recordings/search", CameraRecordingSearchHandler, true},
		{"/cameras/playback/start", CameraPlaybackStartHandler, true},
		{"/cameras/playback/stop", CameraPlaybackStopHandler, true},
		{"/cameras/playback/seek", CameraPlaybackSeekHandler, true},
		{"/cameras/playback/", CameraPlaybackViewHandler, true},
		{"/cameras/export/start", CameraExportStartHandler, true},
		{"/cameras/export/progress/", CameraExportProgressHandler, true},
		{"/cameras/export/download/", CameraExportDownloadHandler, true},
		{"/cameras/export/cancel/", CameraExportCancelHandler, true},
		{"/cameras/exports", CameraExportListHandler, true},
		{"/cameras/dashboard/", CameraGridHandler, true},
		{"/cameras/view/", CameraViewHandler, true},
		{"/cameras/nvr/", CameraListHandler, true},
		{"/cameras/partial", CameraPartialHandler, true},
		{"/cameras", CameraDashboardHandler, true},
		{"/api/cameras", CameraMetricsHandler, true},

		// Heartbeat for session tracking (protected, not admin)
		{"/api/heartbeat", HeartbeatHandler, true},

		// Impersonation status (protected, admin check in handler)
		{"/api/impersonate/status", ImpersonateStatusHandler, true},
		{"/api/allowed-users", AllowedUsersHandler, true},
	}

	// Admin routes (require admin privileges)
	adminRoutes := []AdminRoute{
		{"/admin/kill-switch", AdminKillSwitchHandler},
		{"/admin/exports/debug", CameraExportDebugHandler},
		{"/admin/exports/delete/", CameraExportDeleteHandler},
		{"/admin/exports/purge", CameraExportPurgeHandler},
		{"/admin/exports/retry/", CameraExportRetryHandler},
		{"/admin/exports/restart-worker", CameraExportRestartWorkerHandler},
		{"/admin/impersonate/stop", StopImpersonateHandler},
		{"/admin/impersonate", ImpersonateHandler},
		{"/admin/camera-permissions/toggle", AdminCameraPermissionToggleHandler},
		{"/admin/camera-permissions", AdminCameraPermissionsHandler},
		{"/admin/permissions/toggle", AdminPermissionToggleHandler},
		{"/admin/permissions/grant-all", AdminGrantAllHandler},
		{"/admin/permissions/revoke-all", AdminRevokeAllHandler},
		{"/admin/permissions", AdminPermissionsHandler},
		{"/admin/session/invalidate", AdminInvalidateSessionHandler},
		{"/admin/sessions", AdminSessionsHandler},
		{"/admin/logs/clear", AdminClearLogsHandler},
		{"/admin/logs", AdminLogsHandler},
		{"/admin/stats/unique-users", AdminUniqueUsersHandler},
		{"/admin/stats", AdminStatsHandler},
		{"/admin/notes", AdminNotesHandler},
		{"/admin/docs", AdminDocsHandler},
		{"/admin", AdminDashboardHandler},
	}

	// Register public routes
	for _, r := range publicRoutes {
		http.Handle(r.Pattern, sm.LoadAndSave(http.HandlerFunc(Public(r.Handler))))
	}

	// Register protected routes (with access logging)
	for _, r := range protectedRoutes {
		http.Handle(r.Pattern, sm.LoadAndSave(http.HandlerFunc(ProtectedWithLogging(r.Handler))))
	}

	// Register admin routes
	for _, r := range adminRoutes {
		http.Handle(r.Pattern, sm.LoadAndSave(http.HandlerFunc(AdminProtected(r.Handler))))
	}
}
