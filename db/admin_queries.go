package db

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"cameradashboard/models"
)

// InitAdminTables creates the admin tables if they don't exist
func InitAdminTables() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}
	// Table creation handled by InitSQLiteTables
	log.Printf("Admin tables initialized")
	return nil
}

// ClearAccessLogs deletes all access logs
func ClearAccessLogs() (int64, error) {
	if sqliteDB == nil {
		return 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get count first
	var count int64
	sqliteDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM cmscams_access_logs").Scan(&count)

	_, err := sqliteDB.ExecContext(ctx, "DELETE FROM cmscams_access_logs")
	if err != nil {
		return 0, fmt.Errorf("failed to clear access logs: %w", err)
	}

	return count, nil
}

// InsertAccessLogBatch inserts multiple access log entries in a single batch
func InsertAccessLogBatch(logs []models.AccessLog) error {
	if len(logs) == 0 {
		return nil
	}

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

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO cmscams_access_logs (user_id, user_name, path, method, status_code, client_ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		_, err := stmt.ExecContext(ctx, l.UserID, l.UserName, l.Path, l.Method, l.StatusCode, l.ClientIP, l.UserAgent)
		if err != nil {
			return fmt.Errorf("failed to insert access log: %w", err)
		}
	}

	return tx.Commit()
}

// UpsertActiveSession updates or inserts an active session (heartbeat)
func UpsertActiveSession(session *models.ActiveSession) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		INSERT INTO cmscams_active_sessions
			(session_token, user_id, user_name, email, client_ip, user_agent, current_page, last_activity, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		ON CONFLICT(session_token) DO UPDATE SET
			current_page = excluded.current_page,
			last_activity = datetime('now'),
			client_ip = excluded.client_ip,
			user_agent = excluded.user_agent
	`, session.SessionToken, session.UserID, session.UserName, session.Email,
		session.ClientIP, session.UserAgent, session.CurrentPage)
	if err != nil {
		return fmt.Errorf("failed to upsert session: %w", err)
	}

	return nil
}

// GetActiveSessions returns sessions with activity within the last N seconds
func GetActiveSessions(withinSeconds int) ([]models.ActiveSession, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	cutoff := time.Now().UTC().Add(-time.Duration(withinSeconds) * time.Second).Format(time.RFC3339)

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT id, session_token, user_id, COALESCE(user_name, ''), COALESCE(email, ''),
			   client_ip, COALESCE(user_agent, ''), COALESCE(current_page, ''),
			   last_activity, created_at
		FROM cmscams_active_sessions
		WHERE last_activity > ?
		ORDER BY last_activity DESC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []models.ActiveSession
	for rows.Next() {
		var s models.ActiveSession
		var lastActivity, createdAt string
		if err := rows.Scan(&s.ID, &s.SessionToken, &s.UserID, &s.UserName, &s.Email,
			&s.ClientIP, &s.UserAgent, &s.CurrentPage, &lastActivity, &createdAt); err != nil {
			log.Printf("Error scanning session: %v", err)
			continue
		}
		s.LastActivity, _ = time.Parse(time.RFC3339, lastActivity)
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		sessions = append(sessions, s)
	}

	return sessions, rows.Err()
}

// GetAccessLogs returns access logs with optional filtering and pagination
func GetAccessLogs(userFilter, pathFilter string, dateFrom, dateTo *time.Time, page, pageSize int) ([]models.AccessLog, int, error) {
	if sqliteDB == nil {
		return nil, 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build WHERE clause
	conditions := []string{"1=1"}
	args := []interface{}{}

	if userFilter != "" {
		conditions = append(conditions, "user_id LIKE ?")
		args = append(args, "%"+userFilter+"%")
	}
	if pathFilter != "" {
		conditions = append(conditions, "path LIKE ?")
		args = append(args, "%"+pathFilter+"%")
	}
	if dateFrom != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, dateFrom.UTC().Format(time.RFC3339))
	}
	if dateTo != nil {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, dateTo.UTC().Format(time.RFC3339))
	}

	whereClause := strings.Join(conditions, " AND ")

	// Get total count
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM cmscams_access_logs WHERE %s`, whereClause)
	var total int
	if err := sqliteDB.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count logs: %w", err)
	}

	// Get paginated results
	offset := (page - 1) * pageSize
	paginatedArgs := append(args, pageSize, offset)

	query := fmt.Sprintf(`
		SELECT id, user_id, COALESCE(user_name, ''), path, method, status_code,
			   client_ip, COALESCE(user_agent, ''), timestamp
		FROM cmscams_access_logs
		WHERE %s
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?
	`, whereClause)

	rows, err := sqliteDB.QueryContext(ctx, query, paginatedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query logs: %w", err)
	}
	defer rows.Close()

	var logs []models.AccessLog
	for rows.Next() {
		var l models.AccessLog
		var ts string
		if err := rows.Scan(&l.ID, &l.UserID, &l.UserName, &l.Path, &l.Method,
			&l.StatusCode, &l.ClientIP, &l.UserAgent, &ts); err != nil {
			log.Printf("Error scanning log: %v", err)
			continue
		}
		l.Timestamp, _ = time.Parse(time.RFC3339, ts)
		logs = append(logs, l)
	}

	return logs, total, rows.Err()
}

// GetDistinctLogUsers returns a sorted list of unique user_id values from access logs
func GetDistinctLogUsers() ([]string, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT DISTINCT user_id FROM cmscams_access_logs
		WHERE user_id IS NOT NULL AND user_id != ''
		ORDER BY user_id
	`)
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

// GetAccessStats returns aggregated access statistics
func GetAccessStats() (*models.AccessStats, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	stats := &models.AccessStats{}

	// Online now (active sessions in last 60 seconds)
	sessions, err := GetActiveSessions(60)
	if err == nil {
		stats.OnlineNow = len(sessions)
	}

	todayStart := time.Now().UTC().Format("2006-01-02")

	// Today's views + unique users
	sqliteDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT user_id) FROM cmscams_access_logs
		WHERE timestamp >= ?
	`, todayStart).Scan(&stats.TodayViews, &stats.TodayUniqueUsers)

	// Top dashboards today
	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT path, COUNT(*) as cnt
		FROM cmscams_access_logs
		WHERE timestamp >= ?
		  AND path NOT LIKE '/api/%'
		  AND path NOT LIKE '%/partial%'
		GROUP BY path
		ORDER BY cnt DESC
		LIMIT 10
	`, todayStart)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d models.DashboardViewCount
			if rows.Scan(&d.Path, &d.Count) == nil {
				stats.TopDashboards = append(stats.TopDashboards, d)
			}
		}
	}

	// Top users today
	rows2, err := sqliteDB.QueryContext(ctx, `
		SELECT user_id, MAX(COALESCE(user_name, user_id)) as user_name, COUNT(*) as cnt
		FROM cmscams_access_logs
		WHERE timestamp >= ?
		GROUP BY user_id
		ORDER BY cnt DESC
		LIMIT 10
	`, todayStart)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var u models.UserViewCount
			if rows2.Scan(&u.UserID, &u.UserName, &u.Count) == nil {
				stats.TopUsers = append(stats.TopUsers, u)
			}
		}
	}

	return stats, nil
}

// GetTodayUniqueUsers returns details for each unique user who accessed the app today
func GetTodayUniqueUsers() ([]models.UniqueUserDetail, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	todayStart := time.Now().UTC().Format("2006-01-02")

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT user_id,
		       MAX(COALESCE(user_name, user_id)) as user_name,
		       COUNT(*) as page_views,
		       MIN(timestamp) as first_seen,
		       MAX(timestamp) as last_seen
		FROM cmscams_access_logs
		WHERE timestamp >= ?
		GROUP BY user_id
		ORDER BY last_seen DESC
	`, todayStart)
	if err != nil {
		return nil, fmt.Errorf("failed to query unique users: %w", err)
	}
	defer rows.Close()

	var users []models.UniqueUserDetail
	for rows.Next() {
		var u models.UniqueUserDetail
		if err := rows.Scan(&u.UserID, &u.UserName, &u.PageViews, &u.FirstSeen, &u.LastSeen); err != nil {
			continue
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// InvalidateSession removes a session from the active sessions table
func InvalidateSession(sessionToken string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_active_sessions WHERE session_token = ?`, sessionToken)
	return err
}

// CleanupStaleSessions removes sessions older than the specified duration
func CleanupStaleSessions(olderThanSeconds int) (int64, error) {
	if sqliteDB == nil {
		return 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	cutoff := time.Now().UTC().Add(-time.Duration(olderThanSeconds) * time.Second).Format(time.RFC3339)
	result, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_active_sessions WHERE last_activity < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PurgeAllSessions deletes every row from both the session store and active
// sessions tracking tables, effectively logging out all users.
func PurgeAllSessions() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_sessions`); err != nil {
		return fmt.Errorf("failed to purge session store: %w", err)
	}
	if _, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_active_sessions`); err != nil {
		return fmt.Errorf("failed to purge active sessions: %w", err)
	}
	return nil
}

// GetDistinctLogPaths returns distinct paths from access logs for filter dropdown
func GetDistinctLogPaths() ([]string, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT DISTINCT path
		FROM cmscams_access_logs
		WHERE timestamp > ?
		  AND path NOT LIKE '/api/%'
		  AND path NOT LIKE '%/partial%'
		ORDER BY path
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}

	return paths, rows.Err()
}
