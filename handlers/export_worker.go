package handlers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

var (
	exportConfig   models.Config
	exportWorkerMu sync.RWMutex
	cancelChans    = make(map[string]chan struct{})
	cancelChansMu  sync.Mutex

	// Debug log ring buffer
	exportLogMu    sync.Mutex
	exportLogLines []ExportLogEntry

	// File logger
	exportLogFile *os.File
)

const maxExportLogLines = 200

// ExportLogEntry is a single timestamped log line from the export worker
type ExportLogEntry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// exportLog appends a message to the debug ring buffer, writes to standard log, and writes to log file
func exportLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)

	exportLogMu.Lock()
	exportLogLines = append(exportLogLines, ExportLogEntry{
		Time:    time.Now(),
		Message: msg,
	})
	if len(exportLogLines) > maxExportLogLines {
		exportLogLines = exportLogLines[len(exportLogLines)-maxExportLogLines:]
	}
	if exportLogFile != nil {
		fmt.Fprintf(exportLogFile, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
	exportLogMu.Unlock()
}

// GetExportDebugLogs returns the recent export log lines, optionally filtered by jobUID
func GetExportDebugLogs(jobUID string) []ExportLogEntry {
	exportLogMu.Lock()
	defer exportLogMu.Unlock()

	if jobUID == "" {
		result := make([]ExportLogEntry, len(exportLogLines))
		copy(result, exportLogLines)
		return result
	}

	var filtered []ExportLogEntry
	for _, entry := range exportLogLines {
		if strings.Contains(entry.Message, jobUID) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// InitExportWorker starts the background export worker and cleanup loop
func InitExportWorker(config *models.Config) {
	exportConfig = *config

	// Apply defaults
	if exportConfig.Export.ExportDir == "" {
		exportConfig.Export.ExportDir = exportDir // from camera_export.go
	}
	if exportConfig.Export.MaxConcurrent <= 0 {
		exportConfig.Export.MaxConcurrent = 2
	}
	if exportConfig.Export.RetentionHours <= 0 {
		exportConfig.Export.RetentionHours = 48
	}

	// Ensure export dir exists
	if err := os.MkdirAll(exportConfig.Export.ExportDir, 0755); err != nil {
		exportLog("Camera Export Worker: Failed to create export dir %s: %v", exportConfig.Export.ExportDir, err)
	}

	// Open log file
	logPath := exportConfig.Export.ExportDir + "/export_worker.log"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Camera Export Worker: Failed to open log file %s: %v", logPath, err)
	} else {
		exportLogFile = f
	}

	// Reset any stuck jobs from a previous crash
	if count, err := db.ResetStuckExportJobs(); err != nil {
		exportLog("Camera Export Worker: Failed to reset stuck jobs: %v", err)
	} else if count > 0 {
		exportLog("Camera Export Worker: Reset %d stuck jobs back to queued", count)
	}

	go exportWorkerLoop()
	go exportCleanupLoop()
	exportLog("Camera Export Worker: Started (maxConcurrent=%d, retentionHours=%d, exportDir=%s)",
		exportConfig.Export.MaxConcurrent, exportConfig.Export.RetentionHours, exportConfig.Export.ExportDir)
}

// exportWorkerLoop polls the DB for queued jobs and processes them
func exportWorkerLoop() {
	defer func() {
		if r := recover(); r != nil {
			exportLog("Camera Export Worker: PANIC in worker loop: %v", r)
		}
	}()

	exportLog("Camera Export Worker: Loop goroutine started")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	tick := 0
	for range ticker.C {
		tick++
		// Check if we're at capacity
		active, err := db.CountActiveExportJobs()
		if err != nil {
			exportLog("Camera Export Worker: Failed to count active jobs: %v", err)
			continue
		}
		if active >= exportConfig.Export.MaxConcurrent {
			if tick%12 == 0 {
				exportLog("Camera Export Worker: At capacity (%d/%d active)", active, exportConfig.Export.MaxConcurrent)
			}
			continue
		}

		// Get next queued job
		job, err := db.GetNextQueuedJob()
		if err != nil {
			exportLog("Camera Export Worker: ERROR getting next job (tick=%d): %v", tick, err)
			continue
		}
		if job == nil {
			// Log every tick for now to debug worker liveness
			queuedCount, qErr := db.CountQueuedExportJobs()
			if tick <= 5 || tick%12 == 0 || queuedCount > 0 {
				exportLog("Camera Export Worker: tick=%d active=%d queued=%d qErr=%v", tick, active, queuedCount, qErr)
			}
			continue // nothing in queue
		}

		exportLog("Camera Export Worker: Picked up job %s (%s / %s)", job.JobUID, job.NVRName, job.CameraName)
		processExportJob(job)
	}
}

// processExportJob runs the full export pipeline for a single job.
// It searches the NVR for recording segments overlapping the requested range,
// downloads each segment, transcodes if needed, uploads to OneDrive, and emails the user.
func processExportJob(job *models.ExportJob) {
	exportLog("Camera Export Worker: Processing job %s (%s / %s, %s to %s)",
		job.JobUID, job.NVRName, job.CameraName, job.StartTime, job.EndTime)

	// Register cancel channel
	cancelCh := make(chan struct{})
	cancelChansMu.Lock()
	cancelChans[job.JobUID] = cancelCh
	cancelChansMu.Unlock()

	defer func() {
		cancelChansMu.Lock()
		delete(cancelChans, job.JobUID)
		cancelChansMu.Unlock()
	}()

	if isCancelled(job.JobUID) {
		return
	}

	nvr := findNVRByID(job.NVRID)
	if nvr == nil {
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "NVR not found: "+job.NVRID)
		return
	}

	if nvr.Username == "" {
		user, pass := getNVRCredentials(*nvr)
		if user == "" {
			db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "No credentials for NVR "+job.NVRID)
			return
		}
		nvr.Username = user
		nvr.Password = pass
	}

	// Override exportDir for this job
	origExportDir := exportDir
	exportDir = exportConfig.Export.ExportDir
	defer func() { exportDir = origExportDir }()

	// --- Step 1: Search NVR for recording segments in the requested range ---
	db.UpdateExportJobStatus(job.JobUID, "downloading", 0, 0, "")

	// Parse start/end times
	rangeStart, _ := time.Parse("2006-01-02T15:04:05", job.StartTime)
	rangeEnd, _ := time.Parse("2006-01-02T15:04:05", job.EndTime)
	if rangeStart.IsZero() {
		rangeStart, _ = time.Parse(time.RFC3339, job.StartTime)
	}
	if rangeEnd.IsZero() {
		rangeEnd, _ = time.Parse(time.RFC3339, job.EndTime)
	}
	if rangeStart.IsZero() || rangeEnd.IsZero() {
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "Invalid time range")
		exportLog("Camera Export Worker: Job %s failed — could not parse times: %s to %s", job.JobUID, job.StartTime, job.EndTime)
		return
	}

	dateStr := rangeStart.Format("2006-01-02")
	searchResult, err := searchNVRRecordings(nvr, job.Channel, dateStr)
	if err != nil {
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "Failed to search recordings: "+err.Error())
		return
	}

	// Filter segments that overlap the requested range (include full segments on both sides)
	var segments []models.RecordingSegment
	for _, seg := range searchResult.Segments {
		if seg.EndTime.After(rangeStart) && seg.StartTime.Before(rangeEnd) {
			segments = append(segments, seg)
		}
	}

	if len(segments) == 0 {
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, fmt.Sprintf("No recordings found for %s to %s", job.StartTime, job.EndTime))
		exportLog("Camera Export Worker: Job %s — no segments found in range", job.JobUID)
		return
	}

	exportLog("Camera Export Worker: Job %s found %d segments overlapping range", job.JobUID, len(segments))

	// --- Step 2: Download and transcode each segment ---
	od := exportConfig.Export.OneDrive
	var segmentFiles []string // paths to downloaded/transcoded segment files
	var totalSize int64
	var lastProgressUpdate time.Time

	onProgress := func(status string, progress int, fileSize int64, errMsg string) {
		if time.Since(lastProgressUpdate) < 2*time.Second && errMsg == "" {
			return
		}
		lastProgressUpdate = time.Now()
		db.UpdateExportJobStatus(job.JobUID, status, progress, fileSize, errMsg)
	}

	// Timing metrics
	jobStartTime := time.Now()
	var totalDownloadTime, totalEncodeTime, totalConcatTime, totalUploadTime time.Duration

	for i, seg := range segments {
		if isCancelled(job.JobUID) {
			cleanupFiles(segmentFiles)
			return
		}

		// Clamp segment times to the user's requested range
		clampedStart := seg.StartTime
		clampedEnd := seg.EndTime
		if clampedStart.Before(rangeStart) {
			clampedStart = rangeStart
		}
		if clampedEnd.After(rangeEnd) {
			clampedEnd = rangeEnd
		}
		segStart := clampedStart.Format("2006-01-02T15:04:05Z")
		segEnd := clampedEnd.Format("2006-01-02T15:04:05Z")
		exportLog("Camera Export Worker: Job %s segment %d/%d: %s to %s (clamped from %s to %s)",
			job.JobUID, i+1, len(segments), segStart, segEnd,
			seg.StartTime.Format("2006-01-02T15:04:05Z"), seg.EndTime.Format("2006-01-02T15:04:05Z"))

		// Progress: download phase = 0-60%, concat = 60-70%, upload = 70-95%
		dlProgress := int(float64(i) / float64(len(segments)) * 60)
		onProgress("downloading", dlProgress, totalSize, "")

		// Create a per-segment sub-job for the download functions
		segJob := &models.ExportJob{
			JobUID:     job.JobUID,
			NVRID:      job.NVRID,
			Channel:    job.Channel,
			CameraName: job.CameraName,
			NVRName:    job.NVRName,
			StartTime:  segStart,
			EndTime:    segEnd,
			Quality:    job.Quality,
			ExportName: job.ExportName,
		}

		segProgress := func(status string, progress int, fileSize int64, errMsg string) {
			segPct := float64(progress) / 100.0
			pct := int((float64(i) + segPct) / float64(len(segments)) * 60)
			if pct > 59 {
				pct = 59
			}
			onProgress(status, pct, totalSize+fileSize, errMsg)
		}

		// Try ISAPI then FFmpeg
		dlStart := time.Now()
		success := runISAPIDownloadQueued(segJob, nvr, segProgress, cancelCh)
		if !success {
			if isCancelled(job.JobUID) {
				cleanupFiles(segmentFiles)
				return
			}
			exportLog("Camera Export Worker: Job %s seg %d ISAPI failed, trying FFmpeg", job.JobUID, i+1)
			runFFmpegExportQueued(segJob, nvr, segProgress, cancelCh)
		}
		dlDuration := time.Since(dlStart)
		totalDownloadTime += dlDuration

		// Find the downloaded file
		outputPath, _, ok := buildExportPath(segJob)
		if !ok {
			exportLog("Camera Export Worker: Job %s seg %d — invalid output path", job.JobUID, i+1)
			continue
		}

		info, statErr := os.Stat(outputPath)
		if statErr != nil || info.Size() < 1024 {
			exportLog("Camera Export Worker: Job %s seg %d — file missing or too small (%v)", job.JobUID, i+1, statErr)
			continue
		}

		segFileSize := info.Size()
		dlSpeed := float64(segFileSize) / 1024 / 1024 / dlDuration.Seconds()
		exportLog("Camera Export Worker: Job %s seg %d downloaded (%d bytes, %s, %.1f MB/s)",
			job.JobUID, i+1, segFileSize, dlDuration.Round(time.Millisecond), dlSpeed)

		// Re-encode H.265 → H.264 if needed
		if exportConfig.Export.EncodeH264 {
			if needsH264Encode(outputPath) {
				exportLog("Camera Export Worker: Job %s seg %d H.265 detected, re-encoding", job.JobUID, i+1)
				onProgress("encoding", dlProgress, totalSize, "")
				encStart := time.Now()
				newPath, newSize, encErr := reencodeH264(outputPath, fmt.Sprintf("%s_seg%d", job.JobUID, i+1), segProgress, cancelCh)
				encDuration := time.Since(encStart)
				totalEncodeTime += encDuration
				if encErr != nil {
					exportLog("Camera Export Worker: Job %s seg %d re-encode failed (%s): %v", job.JobUID, i+1, encDuration.Round(time.Millisecond), encErr)
				} else {
					os.Remove(outputPath)
					outputPath = newPath
					segFileSize = newSize
					exportLog("Camera Export Worker: Job %s seg %d re-encoded (%d bytes, %s)",
						job.JobUID, i+1, newSize, encDuration.Round(time.Millisecond))
				}
			}
		}

		totalSize += segFileSize
		segmentFiles = append(segmentFiles, outputPath)
	}

	if len(segmentFiles) == 0 {
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "No segments could be downloaded")
		exportLog("Camera Export Worker: Job %s failed — no segments downloaded", job.JobUID)
		return
	}

	// --- Step 3: Concatenate segments into single MP4 ---
	// Build the final output filename using the main job (not a segment sub-job)
	finalPath, finalName, ok := buildExportPath(job)
	if !ok {
		cleanupFiles(segmentFiles)
		db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "Invalid output path")
		return
	}

	if len(segmentFiles) > 1 {
		onProgress("encoding", 60, totalSize, "")
		exportLog("Camera Export Worker: Job %s concatenating %d segments into single MP4", job.JobUID, len(segmentFiles))
		concatStart := time.Now()
		concatErr := concatSegments(segmentFiles, finalPath, job.JobUID)
		totalConcatTime = time.Since(concatStart)
		cleanupFiles(segmentFiles) // remove individual segment files

		if concatErr != nil {
			exportLog("Camera Export Worker: Job %s concat failed (%s): %v", job.JobUID, totalConcatTime.Round(time.Millisecond), concatErr)
			db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "Failed to concatenate segments: "+concatErr.Error())
			return
		}

		// Update totalSize with the concatenated file size
		if info, err := os.Stat(finalPath); err == nil {
			totalSize = info.Size()
		}
		exportLog("Camera Export Worker: Job %s concatenated (%d bytes, %s)", job.JobUID, totalSize, totalConcatTime.Round(time.Millisecond))
	} else {
		// Single segment — just rename it
		if segmentFiles[0] != finalPath {
			if err := os.Rename(segmentFiles[0], finalPath); err != nil {
				// Rename failed (cross-device?), try copy
				exportLog("Camera Export Worker: Job %s rename failed, copying: %v", job.JobUID, err)
				if cpErr := copyFile(segmentFiles[0], finalPath); cpErr != nil {
					cleanupFiles(segmentFiles)
					db.UpdateExportJobStatus(job.JobUID, "failed", 0, 0, "Failed to prepare final file: "+cpErr.Error())
					return
				}
				os.Remove(segmentFiles[0])
			}
		}
	}
	onProgress("encoding", 70, totalSize, "")

	// Update the DB with the final file info
	db.UpdateExportJobFile(job.JobUID, finalPath, finalName)

	// --- Step 4: Extract thumbnail from final file ---
	var thumbnailPath string
	thumbPath := filepath.Join(exportConfig.Export.ExportDir, job.JobUID+"_thumb.jpg")
	if err := extractThumbnail(finalPath, thumbPath); err != nil {
		exportLog("Camera Export Worker: Job %s thumbnail extraction failed: %v", job.JobUID, err)
	} else {
		thumbnailPath = thumbPath
		exportLog("Camera Export Worker: Job %s thumbnail captured", job.JobUID)
	}

	// --- Step 5: Upload single file to OneDrive ---
	var shareLink string
	if od != nil && od.TenantID != "" && od.ClientID != "" {
		onProgress("uploading", 70, totalSize, "")
		ulStart := time.Now()
		link, uploadErr := UploadExportToOneDrive(
			od.TenantID, od.ClientID, od.ClientSecret, od.FolderName,
			job.RequestedBy, finalPath, finalName, job.JobUID,
			func(pct int) {
				p := 70 + int(float64(pct)/100.0*25)
				if p > 95 {
					p = 95
				}
				onProgress("uploading", p, totalSize, "")
			},
		)
		totalUploadTime = time.Since(ulStart)
		if uploadErr != nil {
			exportLog("Camera Export Worker: Job %s upload failed (%s): %v", job.JobUID, totalUploadTime.Round(time.Millisecond), uploadErr)
		} else {
			ulSpeed := float64(totalSize) / 1024 / 1024 / totalUploadTime.Seconds()
			shareLink = link
			exportLog("Camera Export Worker: Job %s uploaded (%s, %.1f MB/s): %s",
				job.JobUID, totalUploadTime.Round(time.Millisecond), ulSpeed, link)
		}
	}

	// Store the share link in the DB
	if shareLink != "" {
		db.UpdateExportJobShareLink(job.JobUID, shareLink)
	}

	// Clean up local file after upload
	os.Remove(finalPath)

	// --- Step 6: Email notification ---
	emailCfg := exportConfig.Export.Email
	if emailCfg != nil && emailCfg.From != "" && od != nil && shareLink != "" {
		token, err := getGraphToken(od.TenantID, od.ClientID, od.ClientSecret)
		if err != nil {
			exportLog("Camera Export Worker: Job %s email skipped — token error: %v", job.JobUID, err)
		} else {
			upn, err := resolveUserUPN(token, job.RequestedBy)
			if err != nil {
				exportLog("Camera Export Worker: Job %s email skipped — UPN resolve error for %q: %v", job.JobUID, job.RequestedBy, err)
			} else {
				err = SendExportEmail(
					od.TenantID, od.ClientID, od.ClientSecret,
					emailCfg.From, upn, emailCfg.Subject,
					job.CameraName, job.NVRName, job.StartTime, job.EndTime, shareLink, job.ExportName, thumbnailPath,
				)
				if err != nil {
					exportLog("Camera Export Worker: Email failed for %s: %v", job.JobUID, err)
				} else {
					exportLog("Camera Export Worker: Email sent to %s for %s", upn, job.JobUID)
				}
			}
		}
	}

	// Clean up thumbnail
	if thumbnailPath != "" {
		os.Remove(thumbnailPath)
	}

	// Mark complete
	totalJobTime := time.Since(jobStartTime)
	db.UpdateExportJobStatus(job.JobUID, "complete", 100, totalSize, "")

	// Log timing summary
	sizeMB := float64(totalSize) / 1024 / 1024
	exportLog("Camera Export Worker: Job %s complete — %d segments, %.1f MB, total %s (download %s, encode %s, concat %s, upload %s)",
		job.JobUID, len(segments), sizeMB,
		totalJobTime.Round(time.Second),
		totalDownloadTime.Round(time.Second),
		totalEncodeTime.Round(time.Second),
		totalConcatTime.Round(time.Second),
		totalUploadTime.Round(time.Second),
	)
}

// concatSegments concatenates multiple MP4 files into a single MP4 using ffmpeg's concat demuxer.
func concatSegments(inputPaths []string, outputPath, jobUID string) error {
	if len(inputPaths) == 0 {
		return fmt.Errorf("no input files")
	}
	if len(inputPaths) == 1 {
		return os.Rename(inputPaths[0], outputPath)
	}

	// Write concat list file
	listPath := filepath.Join(exportConfig.Export.ExportDir, jobUID+"_concat.txt")
	var listContent strings.Builder
	for _, p := range inputPaths {
		// ffmpeg concat demuxer requires single-quoted paths with escaping
		escaped := strings.ReplaceAll(p, "'", "'\\''")
		listContent.WriteString(fmt.Sprintf("file '%s'\n", escaped))
	}
	if err := os.WriteFile(listPath, []byte(listContent.String()), 0644); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}
	defer os.Remove(listPath)

	// Estimate total duration for timeout
	var totalDuration float64
	for _, p := range inputPaths {
		totalDuration += probeDuration(p)
	}
	if totalDuration <= 0 {
		totalDuration = 300 // fallback 5 minutes
	}
	timeout := time.Duration(totalDuration+120) * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c", "copy",
		"-movflags", "+faststart",
		"-y",
		outputPath,
	).CombinedOutput()
	if err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("ffmpeg concat: %w (%s)", err, string(out))
	}

	info, err := os.Stat(outputPath)
	if err != nil || info.Size() < 1024 {
		os.Remove(outputPath)
		return fmt.Errorf("concat output too small or missing")
	}

	return nil
}

// cleanupFiles removes a list of files, ignoring errors.
func cleanupFiles(paths []string) {
	for _, p := range paths {
		os.Remove(p)
	}
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// extractThumbnail grabs a single frame from a video file and saves it as a JPEG.
// Seeks 5 seconds in to avoid black frames at the start.
func extractThumbnail(videoPath, outputPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-ss", "5",
		"-i", videoPath,
		"-frames:v", "1",
		"-q:v", "3",
		"-vf", "scale=640:-1",
		"-y",
		outputPath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, string(out))
	}

	info, err := os.Stat(outputPath)
	if err != nil || info.Size() < 100 {
		os.Remove(outputPath)
		return fmt.Errorf("thumbnail too small or missing")
	}
	return nil
}

// isCancelled checks if a job has been cancelled
func isCancelled(jobUID string) bool {
	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		return true
	}
	return job.Status == "cancelled"
}

// RestartExportWorker stops the current worker loop and starts a new one
func RestartExportWorker() {
	exportLog("Camera Export Worker: Restart requested")

	// Reset any stuck jobs
	if count, err := db.ResetStuckExportJobs(); err != nil {
		exportLog("Camera Export Worker: Failed to reset stuck jobs on restart: %v", err)
	} else if count > 0 {
		exportLog("Camera Export Worker: Reset %d stuck jobs back to queued", count)
	}

	go exportWorkerLoop()
	exportLog("Camera Export Worker: New worker loop started")
}

// CancelExportJob signals cancellation for a running export
func CancelExportJob(jobUID string) {
	cancelChansMu.Lock()
	ch, ok := cancelChans[jobUID]
	cancelChansMu.Unlock()
	if ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// needsH264Encode probes a video file and returns true if it's H.265/HEVC
func needsH264Encode(filePath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "csv=p=0",
		filePath,
	).Output()
	if err != nil {
		exportLog("Camera Export Worker: ffprobe failed: %v", err)
		return false
	}

	codec := strings.TrimSpace(string(out))
	exportLog("Camera Export Worker: Detected video codec: %s", codec)
	return codec == "hevc" || codec == "h265"
}

// reencodeH264 re-encodes a video file from H.265 to H.264, reporting progress.
// Returns the new file path and size, or an error.
func reencodeH264(inputPath, jobUID string, onProgress ProgressFunc, cancelCh chan struct{}) (string, int64, error) {
	outputPath := strings.TrimSuffix(inputPath, ".mp4") + "_h264.mp4"

	// Get duration for progress calculation
	duration := probeDuration(inputPath)
	if duration <= 0 {
		duration = 60 // fallback
	}

	processTimeout := time.Duration(duration*5+60) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-i", inputPath,
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-movflags", "+faststart",
		"-y",
		outputPath,
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", 0, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start ffmpeg: %w", err)
	}

	exportLog("Camera Export Worker: Re-encoding %s (duration=%.0fs, PID=%d)", jobUID, duration, cmd.Process.Pid)

	// Monitor cancellation
	go func() {
		for {
			time.Sleep(2 * time.Second)
			select {
			case <-cancelCh:
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			default:
			}
			if isCancelled(jobUID) {
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			}
		}
	}()

	// Parse progress from ffmpeg stderr
	encodeTimeRegex := regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.(\d{2})`)
	scanner := bufio.NewScanner(stderr)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		for i := 0; i < len(data); i++ {
			if data[i] == '\n' || data[i] == '\r' {
				return i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})

	for scanner.Scan() {
		line := scanner.Text()
		matches := encodeTimeRegex.FindStringSubmatch(line)
		if len(matches) == 5 {
			h, _ := strconv.Atoi(matches[1])
			m, _ := strconv.Atoi(matches[2])
			s, _ := strconv.Atoi(matches[3])
			elapsed := float64(h*3600+m*60+s) + float64(mustAtoi(matches[4]))/100.0
			pct := int(elapsed / duration * 100)
			if pct > 99 {
				pct = 99
			}
			onProgress("encoding", pct, 0, "")
		}
	}

	err = cmd.Wait()
	if err != nil {
		os.Remove(outputPath)
		return "", 0, fmt.Errorf("ffmpeg: %w", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return "", 0, fmt.Errorf("stat output: %w", err)
	}

	return outputPath, info.Size(), nil
}

// probeDuration returns the duration of a video file in seconds
func probeDuration(filePath string) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		filePath,
	).Output()
	if err != nil {
		return 0
	}

	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return d
}

// mustAtoi converts a string to int, returning 0 on error
func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// exportCleanupLoop periodically removes old completed/failed export jobs and their files
func exportCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		paths, err := db.CleanupOldExportJobs(exportConfig.Export.RetentionHours)
		if err != nil {
			exportLog("Camera Export Worker: Cleanup error: %v", err)
			continue
		}
		for _, p := range paths {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				exportLog("Camera Export Worker: Failed to remove file %s: %v", p, err)
			}
		}
		if len(paths) > 0 {
			exportLog("Camera Export Worker: Cleaned up %d old export files", len(paths))
		}
	}
}
