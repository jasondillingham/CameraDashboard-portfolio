package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cameradashboard/db"
	"cameradashboard/handlers"
	"cameradashboard/internal/auth"
	"cameradashboard/models"
	"cameradashboard/rtsp"

	"github.com/alexedwards/scs/v2"
)

const defaultConfigPath = "../configs/mssql_config.json"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Load configuration
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Default to port 8082 if not specified in config
	if config.Server.Port == 0 {
		config.Server.Port = 8082
	}
	if config.Server.RefreshInterval == 0 {
		config.Server.RefreshInterval = 30
	}

	// Connect to MSSQL database (for P21 ERP queries)
	if err := db.Connect(config); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Connect to SQLite database (for application tables: sessions, permissions, exports, etc.)
	sqliteDBPath := "./data/cameradashboard.db"
	if err := db.ConnectSQLite(sqliteDBPath); err != nil {
		log.Fatalf("Failed to connect to SQLite database: %v", err)
	}
	defer db.CloseSQLite()

	// Initialize all SQLite tables
	if err := db.InitSQLiteTables(); err != nil {
		log.Fatalf("Failed to initialize SQLite tables: %v", err)
	}

	// Initialize session manager with SQLite store
	sessionManager := scs.New()
	sessionManager.Store = db.NewSQLiteSessionStore(db.GetSQLiteDB(), 5*time.Minute)
	sessionManager.Lifetime = 168 * time.Hour // 7 days
	sessionManager.IdleTimeout = 0            // Disable idle timeout to avoid per-request DB UPDATE; Lifetime handles expiry
	sessionManager.Cookie.Name = "camera_dashboard_session"
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode

	// Parse session timeouts from config if provided (idle timeout re-enables per-request DB writes)
	if config.Auth.SessionIdleTimeout != "" {
		if d, err := time.ParseDuration(config.Auth.SessionIdleTimeout); err == nil {
			sessionManager.IdleTimeout = d
		}
	}
	if config.Auth.SessionLifetime != "" {
		if d, err := time.ParseDuration(config.Auth.SessionLifetime); err == nil {
			sessionManager.Lifetime = d
		}
	}

	// Initialize LDAP client — prefer dedicated adlogin.json, fall back to mssql_config.json
	ldapCfg := loadADConfig()
	if ldapCfg == nil {
		// Fall back to legacy LDAP settings in mssql_config.json
		log.Printf("No adlogin.json found, using LDAP settings from mssql_config.json")
		ldapCfg = &auth.LDAPConfig{
			Host:     config.LDAP.Host,
			BaseDN:   config.LDAP.BaseDN,
			BindDN:   config.LDAP.BindDN,
			BindPass: config.LDAP.BindPass,
			GroupsDN: config.LDAP.GroupsDN,
		}
	}
	ldapClient := auth.NewLDAPClient(*ldapCfg)
	defer ldapClient.Close()

	// Get allowed users from config
	allowedUsers := config.Auth.AllowedUsers
	if len(allowedUsers) == 0 {
		log.Fatalf("No allowed users configured in config.Auth.AllowedUsers")
	}

	// Initialize auth middleware
	handlers.InitAuth(sessionManager, ldapClient, allowedUsers)

	// Initialize admin users
	adminUsers := config.Auth.AdminUsers
	if len(adminUsers) == 0 {
		log.Printf("WARNING: No admin users configured in config.Auth.AdminUsers — admin features will be unavailable")
	}
	handlers.InitAdminUsers(adminUsers)

	// Initialize kiosk certificate auth if enabled
	if config.Auth.CertAuth != nil && config.Auth.CertAuth.Enabled {
		if err := handlers.InitKioskAuth(config.Auth.CertAuth.KiosksFile, config.Auth.CertAuth.TrustedProxies); err != nil {
			log.Printf("WARNING: Failed to initialize kiosk auth: %v", err)
		}
	}

	// Load templates
	tmpl, err := loadTemplates()
	if err != nil {
		log.Fatalf("Failed to load templates: %v", err)
	}

	// Initialize handlers
	handlers.Init(tmpl, config.Server.RefreshInterval)
	handlers.SetVersion(GetVersion())

	// Initialize Camera dashboard
	handlers.InitCameraDashboard()

	// Initialize export worker
	handlers.InitExportWorker(config)

	// Start RTSP relay for playback speed control
	rtspRelay := rtsp.NewRelay(15554)
	go rtspRelay.Start()
	handlers.SetRTSPRelay(rtspRelay)

	// Seed allowed users from DB: anyone with "cameras" permission can log in
	if dbUsers, err := db.GetUsersWithPermission("cameras"); err == nil {
		for _, u := range dbUsers {
			handlers.SetUserAllowed(u, true)
		}
		log.Printf("Seeded %d additional allowed users from database permissions", len(dbUsers))
	}

	handlers.InitAccessLogger()
	handlers.InitAdminDashboard()

	// Register go2rtc reverse proxy (before session-wrapped routes so WebSocket works)
	handlers.RegisterGo2RTCProxy()

	// Register all routes
	handlers.RegisterRoutes(sessionManager)

	// Start version watcher for auto-restart on VERSION file changes
	StartVersionWatcher()

	// Start server
	addr := fmt.Sprintf(":%d", config.Server.Port)
	log.Printf("Starting Camera Dashboard server on http://localhost%s (version: %s)", addr, GetVersion())
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// adLoginJSON maps the standard adlogin.json fields used across all projects.
type adLoginJSON struct {
	Server       string `json:"server"`
	Port         int    `json:"port"`
	BindDN       string `json:"bind_dn"`
	BindPassword string `json:"bind_password"`
	BaseDN       string `json:"base_dn"`
}

// loadADConfig tries to load a dedicated adlogin.json config file.
// Returns nil if no file is found (caller should fall back to mssql_config.json).
func loadADConfig() *auth.LDAPConfig {
	paths := []string{
		os.Getenv("AD_CONFIG_PATH"),
		"./config/camera_adlogin.json",
		"../configs/camera_adlogin.json",
		"./config/adlogin.json",
		"../configs/adlogin.json",
	}

	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var ad adLoginJSON
		if err := json.Unmarshal(data, &ad); err != nil {
			log.Printf("Warning: failed to parse %s: %v", p, err)
			continue
		}
		port := ad.Port
		if port == 0 {
			port = 389
		}
		log.Printf("Loaded AD config from: %s (service account: %s)", p, ad.BindDN)
		return &auth.LDAPConfig{
			Host:     fmt.Sprintf("ldap://%s:%d", ad.Server, port),
			BaseDN:   ad.BaseDN,
			BindDN:   ad.BindDN,
			BindPass: ad.BindPassword,
			GroupsDN: ad.BaseDN,
		}
	}

	return nil
}

func loadConfig() (*models.Config, error) {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		// Try to find config relative to executable or working directory
		paths := []string{
			defaultConfigPath,
			"./config/mssql_config.json",
			"../configs/mssql_config.json",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
	}

	if configPath == "" {
		return nil, fmt.Errorf("config file not found")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config models.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	log.Printf("Loaded config from: %s", configPath)
	return &config, nil
}

func loadTemplates() (*template.Template, error) {
	funcMap := template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"multiply": func(a, b int) int {
			return a * b
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"divf": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"trimPrefix": func(prefix, s string) string {
			return strings.TrimPrefix(s, prefix)
		},
		"lower": func(s string) string {
			return strings.ToLower(s)
		},
		"version": func() string {
			return GetVersion()
		},
		"mod": func(a, b int) int {
			return a % b
		},
		"seq": func(start, end int) []int {
			if end < start {
				return []int{}
			}
			result := make([]int, end-start+1)
			for i := range result {
				result[i] = start + i
			}
			return result
		},
		"channels": func(n int) []int {
			result := make([]int, n)
			for i := range result {
				result[i] = i + 1
			}
			return result
		},
		"min": func(a, b int) int {
			if a < b {
				return a
			}
			return b
		},
	}

	templateDirs := []string{
		"templates",
		"./templates",
	}

	var templateDir string
	for _, dir := range templateDirs {
		if _, err := os.Stat(dir); err == nil {
			templateDir = dir
			break
		}
	}

	if templateDir == "" {
		return nil, fmt.Errorf("templates directory not found")
	}

	pattern := filepath.Join(templateDir, "*.html")
	tmpl, err := template.New("").Funcs(funcMap).ParseGlob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	partialsPattern := filepath.Join(templateDir, "partials", "*.html")
	tmpl, err = tmpl.ParseGlob(partialsPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to parse partials: %w", err)
	}

	log.Printf("Loaded templates from: %s", templateDir)
	return tmpl, nil
}
