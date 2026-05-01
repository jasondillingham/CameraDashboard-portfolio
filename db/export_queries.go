package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"cameradashboard/models"
)

// InitExportTables creates the cmscams_export_queue table if it doesn't exist
func InitExportTables() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}
	// Table creation handled by InitSQLiteTables
	log.Printf("Camera Export: Export queue table initialized")
	return nil
}

// InsertExportJob adds a new export job to the queue
func InsertExportJob(job *models.ExportJob) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		INSERT INTO cmscams_export_queue
			(job_uid, nvr_id, channel, camera_name, nvr_name, start_time, end_time, quality, status, requested_by, export_name)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.JobUID, job.NVRID, job.Channel, job.CameraName, job.NVRName,
		job.StartTime, job.EndTime, job.Quality, job.Status, job.RequestedBy, job.ExportName)

	return err
}

// GetExportJobByUID returns a single export job by its UID
func GetExportJobByUID(uid string) (*models.ExportJob, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	job := &models.ExportJob{}
	var filePath, fileName, errorMsg, shareLink, exportName sql.NullString
	var completedAt, createdAt, updatedAt sql.NullString

	err := sqliteDB.QueryRowContext(ctx, `
		SELECT id, job_uid, nvr_id, channel, camera_name, nvr_name,
			start_time, end_time, quality, status, progress,
			file_path, file_name, file_size, error_msg, share_link,
			requested_by, created_at, updated_at, completed_at, export_name
		FROM cmscams_export_queue
		WHERE job_uid = ?
	`, uid).Scan(
		&job.ID, &job.JobUID, &job.NVRID, &job.Channel, &job.CameraName, &job.NVRName,
		&job.StartTime, &job.EndTime, &job.Quality, &job.Status, &job.Progress,
		&filePath, &fileName, &job.FileSize, &errorMsg, &shareLink,
		&job.RequestedBy, &createdAt, &updatedAt, &completedAt, &exportName,
	)
	if err != nil {
		return nil, err
	}

	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	if filePath.Valid {
		job.FilePath = filePath.String
	}
	if fileName.Valid {
		job.FileName = fileName.String
	}
	if errorMsg.Valid {
		job.Error = errorMsg.String
	}
	if shareLink.Valid {
		job.ShareLink = shareLink.String
	}
	if completedAt.Valid && completedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		if !t.IsZero() {
			job.CompletedAt = &t
		}
	}
	if exportName.Valid {
		job.ExportName = exportName.String
	}
	return job, nil
}

// GetExportJobsByUser returns export jobs for a specific user, newest first
func GetExportJobsByUser(username string, limit int) ([]models.ExportJob, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	if limit <= 0 {
		limit = 50
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT id, job_uid, nvr_id, channel, camera_name, nvr_name,
			start_time, end_time, quality, status, progress,
			file_path, file_name, file_size, error_msg, share_link,
			requested_by, created_at, updated_at, completed_at, export_name
		FROM cmscams_export_queue
		WHERE requested_by = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, username, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanExportJobs(rows)
}

// GetAllExportJobs returns all export jobs (admin view), newest first
func GetAllExportJobs(limit int) ([]models.ExportJob, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	if limit <= 0 {
		limit = 100
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT id, job_uid, nvr_id, channel, camera_name, nvr_name,
			start_time, end_time, quality, status, progress,
			file_path, file_name, file_size, error_msg, share_link,
			requested_by, created_at, updated_at, completed_at, export_name
		FROM cmscams_export_queue
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanExportJobs(rows)
}

// GetNextQueuedJob returns the oldest queued job whose NVR doesn't already have an active job, or nil if none
func GetNextQueuedJob() (*models.ExportJob, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	job := &models.ExportJob{}
	var filePath, fileName, errorMsg, shareLink, exportName sql.NullString
	var completedAt, createdAt, updatedAt sql.NullString

	err := sqliteDB.QueryRowContext(ctx, `
		SELECT id, job_uid, nvr_id, channel, camera_name, nvr_name,
			start_time, end_time, quality, status, progress,
			file_path, file_name, file_size, error_msg, share_link,
			requested_by, created_at, updated_at, completed_at, export_name
		FROM cmscams_export_queue
		WHERE status = 'queued'
		AND nvr_id NOT IN (
			SELECT DISTINCT nvr_id FROM cmscams_export_queue
			WHERE status IN ('downloading', 'encoding', 'uploading', 'processing')
		)
		ORDER BY created_at ASC
		LIMIT 1
	`).Scan(
		&job.ID, &job.JobUID, &job.NVRID, &job.Channel, &job.CameraName, &job.NVRName,
		&job.StartTime, &job.EndTime, &job.Quality, &job.Status, &job.Progress,
		&filePath, &fileName, &job.FileSize, &errorMsg, &shareLink,
		&job.RequestedBy, &createdAt, &updatedAt, &completedAt, &exportName,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	if filePath.Valid {
		job.FilePath = filePath.String
	}
	if fileName.Valid {
		job.FileName = fileName.String
	}
	if errorMsg.Valid {
		job.Error = errorMsg.String
	}
	if shareLink.Valid {
		job.ShareLink = shareLink.String
	}
	if completedAt.Valid && completedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		if !t.IsZero() {
			job.CompletedAt = &t
		}
	}
	if exportName.Valid {
		job.ExportName = exportName.String
	}
	return job, nil
}

// UpdateExportJobStatus updates the status, progress, file size, and error for a job
func UpdateExportJobStatus(uid, status string, progress int, fileSize int64, errMsg string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	var completedAt interface{}
	if status == "complete" || status == "failed" {
		completedAt = time.Now().UTC().Format(time.RFC3339)
	}

	_, err := sqliteDB.ExecContext(ctx, `
		UPDATE cmscams_export_queue
		SET status = ?, progress = ?, file_size = ?, error_msg = ?,
			updated_at = datetime('now'), completed_at = ?
		WHERE job_uid = ?
	`, status, progress, fileSize, errMsg, completedAt, uid)
	return err
}

// UpdateExportJobFile updates the file path and name for a job
func UpdateExportJobFile(uid, filePath, fileName string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		UPDATE cmscams_export_queue
		SET file_path = ?, file_name = ?, updated_at = datetime('now')
		WHERE job_uid = ?
	`, filePath, fileName, uid)
	return err
}

// UpdateExportJobShareLink sets the OneDrive share link for a completed job
func UpdateExportJobShareLink(uid, shareLink string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	_, err := sqliteDB.ExecContext(ctx, `
		UPDATE cmscams_export_queue
		SET share_link = ?, updated_at = datetime('now')
		WHERE job_uid = ?
	`, shareLink, uid)
	return err
}

// CountQueuedExportJobs returns the number of jobs waiting to be picked up
func CountQueuedExportJobs() (int, error) {
	if sqliteDB == nil {
		return 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	var count int
	err := sqliteDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cmscams_export_queue
		WHERE status = 'queued'
	`).Scan(&count)
	return count, err
}

// CountActiveExportJobs returns the number of jobs currently in progress
func CountActiveExportJobs() (int, error) {
	if sqliteDB == nil {
		return 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	var count int
	err := sqliteDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cmscams_export_queue
		WHERE status IN ('downloading', 'encoding', 'uploading', 'processing')
	`).Scan(&count)
	return count, err
}

// CleanupOldExportJobs removes completed/failed jobs older than the specified hours.
// Returns file paths of deleted jobs so the caller can remove files.
func CleanupOldExportJobs(olderThanHours int) ([]string, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	cutoff := time.Now().UTC().Add(-time.Duration(olderThanHours) * time.Hour).Format(time.RFC3339)

	// First get file paths for jobs we're about to delete
	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT file_path FROM cmscams_export_queue
		WHERE status IN ('complete', 'failed')
		AND completed_at IS NOT NULL
		AND completed_at < ?
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var fp sql.NullString
		if err := rows.Scan(&fp); err == nil && fp.Valid && fp.String != "" {
			paths = append(paths, fp.String)
		}
	}

	// Delete the jobs
	_, err = sqliteDB.ExecContext(ctx, `
		DELETE FROM cmscams_export_queue
		WHERE status IN ('complete', 'failed')
		AND completed_at IS NOT NULL
		AND completed_at < ?
	`, cutoff)

	return paths, err
}

// DeleteExportJob deletes a single export job by UID and returns its file path for cleanup
func DeleteExportJob(uid string) (string, error) {
	if sqliteDB == nil {
		return "", fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	// Get file path before deleting
	var fp sql.NullString
	sqliteDB.QueryRowContext(ctx, `SELECT file_path FROM cmscams_export_queue WHERE job_uid = ?`, uid).Scan(&fp)

	_, err := sqliteDB.ExecContext(ctx, `DELETE FROM cmscams_export_queue WHERE job_uid = ?`, uid)
	if err != nil {
		return "", err
	}

	if fp.Valid {
		return fp.String, nil
	}
	return "", nil
}

// PurgeCompletedExportJobs deletes all completed/failed jobs and returns their file paths
func PurgeCompletedExportJobs() ([]string, error) {
	if sqliteDB == nil {
		return nil, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	rows, err := sqliteDB.QueryContext(ctx, `
		SELECT file_path FROM cmscams_export_queue
		WHERE status IN ('complete', 'failed')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var fp sql.NullString
		if err := rows.Scan(&fp); err == nil && fp.Valid && fp.String != "" {
			paths = append(paths, fp.String)
		}
	}

	_, err = sqliteDB.ExecContext(ctx, `
		DELETE FROM cmscams_export_queue
		WHERE status IN ('complete', 'failed')
	`)

	return paths, err
}

// ResetExportJobForRetry atomically resets a completed/failed job back to queued,
// clearing all result fields in a single update to avoid race conditions with the worker.
func ResetExportJobForRetry(uid string) error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	// Log current state before update
	var currentStatus string
	sqliteDB.QueryRowContext(ctx, `SELECT status FROM cmscams_export_queue WHERE job_uid = ?`, uid).Scan(&currentStatus)
	log.Printf("Camera Export: ResetForRetry uid=%s currentStatus=%s", uid, currentStatus)

	result, err := sqliteDB.ExecContext(ctx, `
		UPDATE cmscams_export_queue
		SET status = 'queued', progress = 0, file_size = 0, error_msg = '',
			file_path = '', file_name = '', share_link = '',
			completed_at = NULL, updated_at = datetime('now')
		WHERE job_uid = ?
	`, uid)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	log.Printf("Camera Export: ResetForRetry uid=%s rowsAffected=%d", uid, rows)

	// Verify the update actually took effect
	var newStatus string
	sqliteDB.QueryRowContext(ctx, `SELECT status FROM cmscams_export_queue WHERE job_uid = ?`, uid).Scan(&newStatus)
	log.Printf("Camera Export: ResetForRetry uid=%s newStatus=%s", uid, newStatus)

	if newStatus != "queued" {
		return fmt.Errorf("update did not persist: status is '%s' instead of 'queued'", newStatus)
	}

	return nil
}

// ResetStuckExportJobs resets any downloading/encoding/uploading jobs back to queued
// (called on server startup to recover from crashes)
func ResetStuckExportJobs() (int64, error) {
	if sqliteDB == nil {
		return 0, fmt.Errorf("SQLite database not connected")
	}

	ctx, cancel := queryContext()
	defer cancel()

	result, err := sqliteDB.ExecContext(ctx, `
		UPDATE cmscams_export_queue
		SET status = 'queued', progress = 0, updated_at = datetime('now')
		WHERE status IN ('downloading', 'encoding', 'uploading', 'processing')
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// scanExportJobs scans rows into a slice of ExportJob
func scanExportJobs(rows *sql.Rows) ([]models.ExportJob, error) {
	var jobs []models.ExportJob
	for rows.Next() {
		var job models.ExportJob
		var filePath, fileName, errorMsg, shareLink, exportName sql.NullString
		var completedAt, createdAt, updatedAt sql.NullString

		if err := rows.Scan(
			&job.ID, &job.JobUID, &job.NVRID, &job.Channel, &job.CameraName, &job.NVRName,
			&job.StartTime, &job.EndTime, &job.Quality, &job.Status, &job.Progress,
			&filePath, &fileName, &job.FileSize, &errorMsg, &shareLink,
			&job.RequestedBy, &createdAt, &updatedAt, &completedAt, &exportName,
		); err != nil {
			return nil, err
		}

		job.CreatedAt = parseTime(createdAt)
		job.UpdatedAt = parseTime(updatedAt)
		if filePath.Valid {
			job.FilePath = filePath.String
		}
		if fileName.Valid {
			job.FileName = fileName.String
		}
		if errorMsg.Valid {
			job.Error = errorMsg.String
		}
		if shareLink.Valid {
			job.ShareLink = shareLink.String
		}
		if completedAt.Valid && completedAt.String != "" {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			if !t.IsZero() {
				job.CompletedAt = &t
			}
		}
		if exportName.Valid {
			job.ExportName = exportName.String
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// parseTime parses a NullString time value from SQLite
func parseTime(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	// Try multiple formats SQLite might store
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			return t
		}
	}
	return time.Time{}
}
