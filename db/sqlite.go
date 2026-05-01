package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var sqliteDB *sql.DB

// ConnectSQLite opens (or creates) the local SQLite database at dbPath.
// It enables WAL mode and creates the data directory if needed.
func ConnectSQLite(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", dir, err)
	}

	var err error
	sqliteDB, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return fmt.Errorf("failed to open SQLite database: %w", err)
	}

	sqliteDB.SetMaxOpenConns(1) // SQLite handles one writer at a time
	sqliteDB.SetMaxIdleConns(1)

	if err := sqliteDB.Ping(); err != nil {
		return fmt.Errorf("failed to ping SQLite database: %w", err)
	}

	log.Printf("Connected to SQLite database: %s", dbPath)
	return nil
}

// GetSQLiteDB returns the SQLite database connection
func GetSQLiteDB() *sql.DB {
	return sqliteDB
}

// CloseSQLite closes the SQLite database connection
func CloseSQLite() {
	if sqliteDB != nil {
		sqliteDB.Close()
	}
}

// InitSQLiteTables creates all application tables in SQLite
func InitSQLiteTables() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	if err := initSessionsTableSQLite(); err != nil {
		return fmt.Errorf("sessions table: %w", err)
	}
	if err := initAdminTablesSQLite(); err != nil {
		return fmt.Errorf("admin tables: %w", err)
	}
	if err := initPermissionTablesSQLite(); err != nil {
		return fmt.Errorf("permission tables: %w", err)
	}
	if err := initCameraPermissionTablesSQLite(); err != nil {
		return fmt.Errorf("camera permission tables: %w", err)
	}
	if err := initExportTablesSQLite(); err != nil {
		return fmt.Errorf("export tables: %w", err)
	}

	log.Printf("All SQLite tables initialized")
	return nil
}

func initSessionsTableSQLite() error {
	_, err := sqliteDB.Exec(`
		CREATE TABLE IF NOT EXISTS cd_sessions (
			token  TEXT PRIMARY KEY,
			data   BLOB NOT NULL,
			expiry TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON cd_sessions(expiry);
	`)
	return err
}

func initAdminTablesSQLite() error {
	_, err := sqliteDB.Exec(`
		CREATE TABLE IF NOT EXISTS cd_access_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			user_name TEXT,
			path TEXT NOT NULL,
			method TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			client_ip TEXT NOT NULL,
			user_agent TEXT,
			timestamp TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_access_logs_timestamp ON cd_access_logs(timestamp);
		CREATE INDEX IF NOT EXISTS idx_access_logs_user_id ON cd_access_logs(user_id);
		CREATE INDEX IF NOT EXISTS idx_access_logs_path ON cd_access_logs(path);

		CREATE TABLE IF NOT EXISTS cd_active_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_token TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			user_name TEXT,
			email TEXT,
			client_ip TEXT NOT NULL,
			user_agent TEXT,
			current_page TEXT,
			last_activity TEXT NOT NULL DEFAULT (datetime('now')),
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_active_sessions_last_activity ON cd_active_sessions(last_activity);
		CREATE INDEX IF NOT EXISTS idx_active_sessions_user_id ON cd_active_sessions(user_id);
	`)
	return err
}

func initPermissionTablesSQLite() error {
	_, err := sqliteDB.Exec(`
		CREATE TABLE IF NOT EXISTS cd_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			dashboard_slug TEXT NOT NULL,
			granted_by TEXT NOT NULL,
			granted_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE (user_id, dashboard_slug)
		);
		CREATE INDEX IF NOT EXISTS idx_perms_user ON cd_permissions(user_id);
		CREATE INDEX IF NOT EXISTS idx_perms_slug ON cd_permissions(dashboard_slug);
	`)
	return err
}

func initCameraPermissionTablesSQLite() error {
	_, err := sqliteDB.Exec(`
		CREATE TABLE IF NOT EXISTS cd_camera_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			nvr_id TEXT NOT NULL,
			channel INTEGER NOT NULL DEFAULT 0,
			UNIQUE (user_id, nvr_id, channel)
		);
		CREATE INDEX IF NOT EXISTS idx_cam_perm_user ON cd_camera_permissions(user_id);

		CREATE TABLE IF NOT EXISTS cd_preset_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			preset_id TEXT NOT NULL,
			UNIQUE (user_id, preset_id)
		);
		CREATE INDEX IF NOT EXISTS idx_cam_preset_perm_user ON cd_preset_permissions(user_id);
	`)
	return err
}

func initExportTablesSQLite() error {
	_, err := sqliteDB.Exec(`
		CREATE TABLE IF NOT EXISTS cd_export_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_uid TEXT NOT NULL UNIQUE,
			nvr_id TEXT NOT NULL,
			channel INTEGER NOT NULL,
			camera_name TEXT,
			nvr_name TEXT,
			start_time TEXT,
			end_time TEXT,
			quality TEXT,
			status TEXT NOT NULL DEFAULT 'queued',
			progress INTEGER NOT NULL DEFAULT 0,
			file_path TEXT,
			file_name TEXT,
			file_size INTEGER NOT NULL DEFAULT 0,
			error_msg TEXT,
			share_link TEXT,
			export_name TEXT,
			requested_by TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			completed_at TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_export_status ON cd_export_queue(status);
		CREATE INDEX IF NOT EXISTS idx_export_user ON cd_export_queue(requested_by);
		CREATE INDEX IF NOT EXISTS idx_export_created ON cd_export_queue(created_at);
	`)
	return err
}
