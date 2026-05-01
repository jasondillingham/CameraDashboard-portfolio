package db

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Permission cache with TTL
var permCacheStore = newCache(30*time.Second, loadPermissions)

// InitPermissionTables creates the cd_permissions table if it doesn't exist
func InitPermissionTables() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}
	// Table creation handled by InitSQLiteTables
	log.Printf("Permission tables initialized")
	return nil
}

// loadPermissions loads all permissions from DB (used as cache load function)
func loadPermissions() (map[string]map[string]bool, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT user_id, dashboard_slug
		FROM cd_permissions
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to load permissions: %w", err)
	}
	defer rows.Close()

	cache := make(map[string]map[string]bool)
	for rows.Next() {
		var userID, slug string
		if err := rows.Scan(&userID, &slug); err != nil {
			continue
		}
		userID = strings.ToLower(userID)
		if cache[userID] == nil {
			cache[userID] = make(map[string]bool)
		}
		cache[userID][slug] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return cache, nil
}

// GetUsersWithPermission returns all user IDs that have a specific permission slug
func GetUsersWithPermission(slug string) ([]string, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT LOWER(user_id) FROM cd_permissions
		WHERE dashboard_slug = ?
	`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil {
			users = append(users, u)
		}
	}
	return users, rows.Err()
}

// UserHasPermission checks if a user has access to a dashboard slug
func UserHasPermission(userID, slug string) (bool, error) {
	cache, err := permCacheStore.get()
	if err != nil {
		return false, err
	}

	userSlugs, ok := cache[strings.ToLower(userID)]
	if !ok {
		return false, nil
	}
	return userSlugs[slug], nil
}

// GetUserPermissions returns all granted slugs for a user
func GetUserPermissions(userID string) (map[string]bool, error) {
	cache, err := permCacheStore.get()
	if err != nil {
		return nil, err
	}

	result := cache[strings.ToLower(userID)]
	if result == nil {
		return make(map[string]bool), nil
	}
	return result, nil
}

// GrantPermission grants a user access to a dashboard
func GrantPermission(userID, slug, grantedBy string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO cd_permissions (user_id, dashboard_slug, granted_by)
		VALUES (?, ?, ?)
	`, strings.ToLower(userID), slug, grantedBy)
	if err != nil {
		return fmt.Errorf("failed to grant permission: %w", err)
	}

	permCacheStore.invalidate()
	return nil
}

// RevokePermission revokes a user's access to a dashboard
func RevokePermission(userID, slug string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		DELETE FROM cd_permissions WHERE user_id = ? AND dashboard_slug = ?
	`, strings.ToLower(userID), slug)
	if err != nil {
		return fmt.Errorf("failed to revoke permission: %w", err)
	}

	permCacheStore.invalidate()
	return nil
}

// GrantAllPermissions grants a user access to all provided slugs
func GrantAllPermissions(userID string, slugs []string, grantedBy string) error {
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

	for _, slug := range slugs {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO cd_permissions (user_id, dashboard_slug, granted_by)
			VALUES (?, ?, ?)
		`, strings.ToLower(userID), slug, grantedBy)
		if err != nil {
			return fmt.Errorf("failed to grant permission for %s: %w", slug, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	permCacheStore.invalidate()
	return nil
}

// RevokeAllPermissions revokes all dashboard permissions for a user
func RevokeAllPermissions(userID string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		DELETE FROM cd_permissions WHERE user_id = ?
	`, strings.ToLower(userID))
	if err != nil {
		return fmt.Errorf("failed to revoke all permissions: %w", err)
	}

	permCacheStore.invalidate()
	return nil
}
