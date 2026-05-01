package db

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Camera permission caches with TTL
var (
	camPermCacheStore       = newCache(30*time.Second, loadCamPermissions)
	camPresetPermCacheStore = newCache(30*time.Second, loadCamPresetPermissions)
)

// InitCameraPermissionTables creates cmscams_camera_permissions and cmscams_preset_permissions tables
func InitCameraPermissionTables() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}
	// Table creation handled by InitSQLiteTables
	log.Printf("Camera permission tables initialized")
	return nil
}

// invalidateCamPermCache clears both camera permission caches
func invalidateCamPermCache() {
	camPermCacheStore.invalidate()
	camPresetPermCacheStore.invalidate()
}

// loadCamPermissions loads all camera permissions from DB (cache load function)
func loadCamPermissions() (map[string]map[string]map[int]bool, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT user_id, nvr_id, channel
		FROM cmscams_camera_permissions
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to load camera permissions: %w", err)
	}
	defer rows.Close()

	cache := make(map[string]map[string]map[int]bool)
	for rows.Next() {
		var userID, nvrID string
		var channel int
		if err := rows.Scan(&userID, &nvrID, &channel); err != nil {
			continue
		}
		userID = strings.ToLower(userID)
		if cache[userID] == nil {
			cache[userID] = make(map[string]map[int]bool)
		}
		if cache[userID][nvrID] == nil {
			cache[userID][nvrID] = make(map[int]bool)
		}
		cache[userID][nvrID][channel] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cache, nil
}

// loadCamPresetPermissions loads all preset permissions from DB (cache load function)
func loadCamPresetPermissions() (map[string]map[string]bool, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT user_id, preset_id
		FROM cmscams_preset_permissions
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to load camera preset permissions: %w", err)
	}
	defer rows.Close()

	cache := make(map[string]map[string]bool)
	for rows.Next() {
		var userID, presetID string
		if err := rows.Scan(&userID, &presetID); err != nil {
			continue
		}
		userID = strings.ToLower(userID)
		if cache[userID] == nil {
			cache[userID] = make(map[string]bool)
		}
		cache[userID][presetID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cache, nil
}

// UserHasNVRAccess checks if a user has access to any camera on a given NVR
func UserHasNVRAccess(userID, nvrID string) (bool, error) {
	cache, err := camPermCacheStore.get()
	if err != nil {
		return false, err
	}

	nvrPerms, ok := cache[strings.ToLower(userID)]
	if !ok {
		return false, nil
	}
	channels, ok := nvrPerms[nvrID]
	if !ok {
		return false, nil
	}
	return len(channels) > 0, nil
}

// UserHasCameraAccess checks if a user can view a specific channel on an NVR
// Returns true if user has channel=0 (all) grant or a specific channel grant
func UserHasCameraAccess(userID, nvrID string, channel int) (bool, error) {
	cache, err := camPermCacheStore.get()
	if err != nil {
		return false, err
	}

	nvrPerms, ok := cache[strings.ToLower(userID)]
	if !ok {
		return false, nil
	}
	channels, ok := nvrPerms[nvrID]
	if !ok {
		return false, nil
	}
	// channel=0 means all cameras
	if channels[0] {
		return true, nil
	}
	return channels[channel], nil
}

// GetUserCameraPermissions returns all NVR/channel grants for a user
func GetUserCameraPermissions(userID string) (map[string]map[int]bool, error) {
	cache, err := camPermCacheStore.get()
	if err != nil {
		return nil, err
	}

	result := cache[strings.ToLower(userID)]
	if result == nil {
		return make(map[string]map[int]bool), nil
	}
	return result, nil
}

// UserHasPresetAccess checks if a user has access to a specific preset
func UserHasPresetAccess(userID, presetID string) (bool, error) {
	cache, err := camPresetPermCacheStore.get()
	if err != nil {
		return false, err
	}

	presets, ok := cache[strings.ToLower(userID)]
	if !ok {
		return false, nil
	}
	return presets[presetID], nil
}

// GetUserPresetPermissions returns all preset grants for a user
func GetUserPresetPermissions(userID string) (map[string]bool, error) {
	cache, err := camPresetPermCacheStore.get()
	if err != nil {
		return nil, err
	}

	result := cache[strings.ToLower(userID)]
	if result == nil {
		return make(map[string]bool), nil
	}
	return result, nil
}

// GrantCameraPermission grants a user access to an NVR channel (channel=0 for all)
func GrantCameraPermission(userID, nvrID string, channel int) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO cmscams_camera_permissions (user_id, nvr_id, channel)
		VALUES (?, ?, ?)
	`, strings.ToLower(userID), nvrID, channel)
	if err != nil {
		return fmt.Errorf("failed to grant camera permission: %w", err)
	}
	invalidateCamPermCache()
	return nil
}

// RevokeCameraPermission revokes a user's access to an NVR channel.
// When channel=0 (all cameras), also revokes all individual channel grants for the NVR.
func RevokeCameraPermission(userID, nvrID string, channel int) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	var err error
	if channel == 0 {
		// Revoking "all cameras" should also clear individual channel grants
		_, err = sqliteDB.ExecContext(ctx, `
			DELETE FROM cmscams_camera_permissions WHERE user_id = ? AND nvr_id = ?
		`, strings.ToLower(userID), nvrID)
	} else {
		_, err = sqliteDB.ExecContext(ctx, `
			DELETE FROM cmscams_camera_permissions WHERE user_id = ? AND nvr_id = ? AND channel = ?
		`, strings.ToLower(userID), nvrID, channel)
	}
	if err != nil {
		return fmt.Errorf("failed to revoke camera permission: %w", err)
	}
	invalidateCamPermCache()
	return nil
}

// GrantAllNVRs grants channel=0 (all cameras) on all provided NVR IDs
func GrantAllNVRs(userID string, nvrIDs []string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := sqliteDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	for _, nvrID := range nvrIDs {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO cmscams_camera_permissions (user_id, nvr_id, channel)
			VALUES (?, ?, 0)
		`, strings.ToLower(userID), nvrID)
		if err != nil {
			return fmt.Errorf("failed to grant NVR %s: %w", nvrID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	invalidateCamPermCache()
	return nil
}

// RevokeAllCameraPermissions revokes all camera and preset permissions for a user
func RevokeAllCameraPermissions(userID string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	uid := strings.ToLower(userID)
	if _, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_camera_permissions WHERE user_id = ?`, uid); err != nil {
		return fmt.Errorf("failed to revoke all camera permissions: %w", err)
	}
	if _, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_preset_permissions WHERE user_id = ?`, uid); err != nil {
		return fmt.Errorf("failed to revoke all preset permissions: %w", err)
	}
	invalidateCamPermCache()
	return nil
}

// GrantPresetPermission grants a user access to a camera preset
func GrantPresetPermission(userID, presetID string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO cmscams_preset_permissions (user_id, preset_id)
		VALUES (?, ?)
	`, strings.ToLower(userID), presetID)
	if err != nil {
		return fmt.Errorf("failed to grant preset permission: %w", err)
	}
	invalidateCamPermCache()
	return nil
}

// RevokePresetPermission revokes a user's access to a camera preset
func RevokePresetPermission(userID, presetID string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		DELETE FROM cmscams_preset_permissions WHERE user_id = ? AND preset_id = ?
	`, strings.ToLower(userID), presetID)
	if err != nil {
		return fmt.Errorf("failed to revoke preset permission: %w", err)
	}
	invalidateCamPermCache()
	return nil
}
