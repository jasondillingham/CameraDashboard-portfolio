package handlers

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"

	"github.com/icholy/digest"
)

var (
	exportDir string

	ffmpegAvailable bool
	timeRegex       = regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.(\d{2})`)
)

// ProgressFunc is called by download functions to report progress
type ProgressFunc func(status string, progress int, fileSize int64, errMsg string)

// initExportDir creates the export temp directory
func initExportDir() {
	exportDir = filepath.Join(os.TempDir(), "dashboard_exports")
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		exportLog("Camera Export: Failed to create export dir: %v", err)
	}

	// Check if FFmpeg is available
	_, err := exec.LookPath("ffmpeg")
	ffmpegAvailable = err == nil
	if !ffmpegAvailable {
		exportLog("Camera Export: WARNING - ffmpeg not found in PATH")
	} else {
		exportLog("Camera Export: ffmpeg found, export feature available")
	}
}

// buildExportPath creates the output file path and validates it.
func buildExportPath(job *models.ExportJob) (outputPath, fileName string, ok bool) {
	replacer := strings.NewReplacer(" ", "_", "/", "-", "\\", "-", ":", "-")
	safeName := replacer.Replace(job.NVRName)
	safeCam := replacer.Replace(job.CameraName)
	safeStart := strings.NewReplacer(":", "", "-", "", "T", "_", "Z", "").Replace(job.StartTime)
	safeEnd := strings.NewReplacer(":", "", "-", "", "T", "_", "Z", "").Replace(job.EndTime)

	if job.ExportName != "" {
		safeExportName := replacer.Replace(job.ExportName)
		fileName = fmt.Sprintf("%s_%s_%s_%s_to_%s.mp4", safeExportName, safeName, safeCam, safeStart, safeEnd)
	} else {
		fileName = fmt.Sprintf("%s_%s_%s_to_%s.mp4", safeName, safeCam, safeStart, safeEnd)
	}
	outputPath = filepath.Join(exportDir, job.JobUID+"_"+fileName)

	absOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return "", "", false
	}
	absExport, err := filepath.Abs(exportDir)
	if err != nil {
		return "", "", false
	}
	if !strings.HasPrefix(absOutput, absExport+string(filepath.Separator)) {
		return "", "", false
	}
	return outputPath, fileName, true
}

// runISAPIDownloadQueued uses the Hikvision ISAPI download endpoint.
// Returns true if handled (success or handled failure), false to fall back to FFmpeg.
func runISAPIDownloadQueued(job *models.ExportJob, nvr *models.NVR, onProgress ProgressFunc, cancelCh chan struct{}) bool {
	user, pass := getNVRCredentials(*nvr)
	if user == "" {
		return false
	}

	outputPath, fileName, ok := buildExportPath(job)
	if !ok {
		return false
	}

	startRTSP := formatISAPIToRTSP(job.StartTime)
	endRTSP := formatISAPIToRTSP(job.EndTime)

	// ISAPI always downloads main stream for best quality
	trackID := fmt.Sprintf("%d01", job.Channel)

	xmlBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<downloadRequest version="1.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<playbackURI>rtsp://%s/Streaming/tracks/%s?starttime=%s&amp;endtime=%s</playbackURI>
</downloadRequest>`, nvr.IP, trackID, startRTSP, endRTSP)

	exportLog("Camera Export: ISAPI XML body: %s", xmlBody)

	downloadURL := fmt.Sprintf("http://%s/ISAPI/ContentMgmt/download", nvr.IP)
	exportLog("Camera Export: Job %s using ISAPI download from %s", job.JobUID, nvr.IP)

	client := &http.Client{
		Timeout: 0,
		Transport: &digest.Transport{
			Username: user,
			Password: pass,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}

	resp, err := client.Post(downloadURL, "application/xml", strings.NewReader(xmlBody))
	if err != nil {
		exportLog("Camera Export: ISAPI download request failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		exportLog("Camera Export: ISAPI download returned %d: %s", resp.StatusCode, string(body))
		return false
	}

	expectedSize := resp.ContentLength

	outFile, err := os.Create(outputPath)
	if err != nil {
		exportLog("Camera Export: Failed to create output file: %v", err)
		return false
	}
	defer outFile.Close()

	db.UpdateExportJobFile(job.JobUID, outputPath, fileName)

	buf := make([]byte, 256*1024)
	var written int64
	for {
		// Check cancellation
		select {
		case <-cancelCh:
			outFile.Close()
			os.Remove(outputPath)
			return true
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := outFile.Write(buf[:n])
			if writeErr != nil {
				onProgress("failed", 0, written, fmt.Sprintf("Write error: %v", writeErr))
				return false
			}
			written += int64(n)

			if expectedSize > 0 {
				pct := int(float64(written) / float64(expectedSize) * 100)
				if pct > 99 {
					pct = 99
				}
				onProgress("downloading", pct, written, "")
			} else {
				onProgress("downloading", 0, written, "")
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				exportLog("Camera Export: ISAPI read error: %v (got %d bytes)", readErr, written)
				if written < 1024 {
					return false
				}
			}
			break
		}
	}

	onProgress("downloading", 99, written, "")
	exportLog("Camera Export: Job %s ISAPI download complete (%s, %d bytes)", job.JobUID, fileName, written)
	return true
}

// runFFmpegExportQueued runs FFmpeg to export a recording to MP4
func runFFmpegExportQueued(job *models.ExportJob, nvr *models.NVR, onProgress ProgressFunc, cancelCh chan struct{}) {
	rtspStart := formatISAPIToRTSP(job.StartTime)
	rtspEnd := formatISAPIToRTSP(job.EndTime)
	rtspURL := nvr.GetPlaybackRTSPURL(job.Channel, false, rtspStart, rtspEnd)

	outputPath, fileName, ok := buildExportPath(job)
	if !ok {
		onProgress("failed", 0, 0, "Invalid output path")
		return
	}

	db.UpdateExportJobFile(job.JobUID, outputPath, fileName)

	startT, _ := time.Parse(time.RFC3339, job.StartTime)
	endT, _ := time.Parse(time.RFC3339, job.EndTime)
	if startT.IsZero() {
		startT, _ = time.Parse("2006-01-02T15:04:05Z", job.StartTime)
	}
	if endT.IsZero() {
		endT, _ = time.Parse("2006-01-02T15:04:05Z", job.EndTime)
	}
	totalDuration := endT.Sub(startT).Seconds()
	if totalDuration <= 0 {
		totalDuration = 1
	}

	maskedURL := strings.Replace(rtspURL, nvr.Password, "***", 1)
	exportLog("Camera Export: Job %s FFmpeg fallback RTSP URL: %s", job.JobUID, maskedURL)

	// Download to MKV first — MKV is a streaming format that stays valid even if FFmpeg is killed
	// (unlike MP4 which needs a moov atom written on clean exit)
	mkvPath := strings.TrimSuffix(outputPath, ".mp4") + ".mkv"

	durationStr := fmt.Sprintf("%.0f", totalDuration+5)
	processTimeout := time.Duration(totalDuration*10+30) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"ffmpeg",
		"-rtsp_transport", "tcp",
		"-t", durationStr,
		"-i", rtspURL,
		"-an",
		"-c:v", "copy",
		"-f", "matroska",
		"-y",
		mkvPath,
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		onProgress("failed", 0, 0, fmt.Sprintf("Failed to create stderr pipe: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		onProgress("failed", 0, 0, fmt.Sprintf("Failed to start FFmpeg: %v", err))
		return
	}

	exportLog("Camera Export: Job %s FFmpeg started (PID %d, duration=%.0fs, timeout=%v, output=%s)",
		job.JobUID, cmd.Process.Pid, totalDuration, processTimeout, mkvPath)

	// Monitor for cancellation + report file size growth + detect stall
	done := make(chan struct{})
	go func() {
		tick := 0
		var lastFileSize int64
		stallTicks := 0
		const stallKillThreshold = 5 // kill after 15s (5 ticks × 3s) of no file growth
		for {
			select {
			case <-done:
				exportLog("Camera Export: Job %s monitor exiting (done signal)", job.JobUID)
				return
			case <-cancelCh:
				exportLog("Camera Export: Job %s monitor detected cancellation", job.JobUID)
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			case <-time.After(3 * time.Second):
			}
			tick++
			// Check if cancelled via DB
			j, err := db.GetExportJobByUID(job.JobUID)
			if err != nil || j == nil || j.Status == "cancelled" {
				exportLog("Camera Export: Job %s monitor killing FFmpeg (db status=%v, err=%v)", job.JobUID, j, err)
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				return
			}
			// Report file size growth
			if info, err := os.Stat(mkvPath); err == nil && info.Size() > 0 {
				currentSize := info.Size()
				if currentSize == lastFileSize && currentSize > 10*1024 {
					stallTicks++
				} else {
					stallTicks = 0
				}
				lastFileSize = currentSize

				exportLog("Camera Export: Job %s tick=%d fileSize=%d progress=%d stall=%d/%d",
					job.JobUID, tick, currentSize, j.Progress, stallTicks, stallKillThreshold)
				onProgress("downloading", j.Progress, currentSize, "")

				// Gracefully stop FFmpeg if file stopped growing — RTSP stream likely finished
				if stallTicks >= stallKillThreshold {
					exportLog("Camera Export: Job %s file stalled at %d bytes for %ds — sending SIGTERM to FFmpeg (RTSP likely done)",
						job.JobUID, currentSize, stallTicks*3)
					// SIGTERM lets FFmpeg finalize the MP4 container (write moov atom)
					// Unlike SIGKILL, FFmpeg handles SIGTERM gracefully
					if cmd.Process != nil {
						cmd.Process.Signal(syscall.SIGTERM)
					}
					// Give it 10s to finalize, then force kill
					go func() {
						time.Sleep(10 * time.Second)
						if cmd.Process != nil {
							exportLog("Camera Export: Job %s force-killing FFmpeg after SIGTERM timeout", job.JobUID)
							cmd.Process.Kill()
						}
					}()
					return
				}
			} else {
				exportLog("Camera Export: Job %s tick=%d file not yet created (err=%v)",
					job.JobUID, tick, err)
			}
		}
	}()

	var lastLines []string
	lineCount := 0
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
		lineCount++
		lastLines = append(lastLines, line)
		if len(lastLines) > 10 {
			lastLines = lastLines[1:]
		}
		// Log first few lines and any time= lines
		matches := timeRegex.FindStringSubmatch(line)
		if lineCount <= 5 {
			exportLog("Camera Export: Job %s stderr[%d]: %s", job.JobUID, lineCount, line)
		}
		if len(matches) == 5 {
			h := parseDigits(matches[1])
			m := parseDigits(matches[2])
			s := parseDigits(matches[3])
			elapsed := float64(h*3600+m*60+s) + float64(parseDigits(matches[4]))/100.0
			progress := int((elapsed / totalDuration) * 100)
			if progress > 99 {
				progress = 99
			}
			exportLog("Camera Export: Job %s time=%s progress=%d%%", job.JobUID, matches[0], progress)
			onProgress("downloading", progress, 0, "")
		}
	}

	exportLog("Camera Export: Job %s stderr scanner finished (%d lines total), waiting for process...", job.JobUID, lineCount)
	if lineCount > 5 {
		exportLog("Camera Export: Job %s last stderr lines:\n%s", job.JobUID, strings.Join(lastLines, "\n"))
	}

	err = cmd.Wait()
	close(done)

	exportLog("Camera Export: Job %s cmd.Wait() returned: err=%v", job.JobUID, err)

	// Check MKV output file regardless of exit code — RTSP streams often exit non-zero
	var mkvSize int64
	if info, statErr := os.Stat(mkvPath); statErr == nil {
		mkvSize = info.Size()
	}
	exportLog("Camera Export: Job %s MKV file size=%d bytes (exit err=%v)", job.JobUID, mkvSize, err)

	if mkvSize < 10*1024 { // <10KB = unusable
		stderrTail := strings.Join(lastLines, "\n")
		onProgress("failed", 0, 0, fmt.Sprintf("FFmpeg error: %v", err))
		exportLog("Camera Export: Job %s failed: %v\nFFmpeg stderr:\n%s", job.JobUID, err, stderrTail)
		os.Remove(mkvPath)
		return
	}

	// Remux MKV → MP4 (MKV is always valid, so this converts to a proper MP4 with moov atom)
	exportLog("Camera Export: Job %s remuxing MKV → MP4", job.JobUID)
	remuxed, mp4Size := remuxToMP4(mkvPath, outputPath, job.JobUID)
	os.Remove(mkvPath) // clean up MKV regardless
	if !remuxed {
		onProgress("failed", 0, 0, "Failed to remux MKV to MP4")
		return
	}

	onProgress("downloading", 99, mp4Size, "")
	exportLog("Camera Export: Job %s remux complete (%s, %d bytes)", job.JobUID, fileName, mp4Size)
}

// remuxToMP4 converts an MKV (or other container) to MP4 with proper moov atom.
// Returns true and the output file size on success.
func remuxToMP4(inputPath, outputPath, jobUID string) (bool, int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-c", "copy",
		"-movflags", "+faststart",
		"-y",
		outputPath,
	).CombinedOutput()
	if err != nil {
		exportLog("Camera Export: Job %s remux failed: %v\n%s", jobUID, err, string(out))
		os.Remove(outputPath)
		return false, 0
	}

	info, err := os.Stat(outputPath)
	if err != nil || info.Size() < 1024 {
		exportLog("Camera Export: Job %s remux output too small or missing", jobUID)
		os.Remove(outputPath)
		return false, 0
	}

	exportLog("Camera Export: Job %s remuxed MKV → MP4 successfully (%d bytes)", jobUID, info.Size())
	return true, info.Size()
}

// parseDigits parses a string of digits into an int
func parseDigits(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// CameraExportStartHandler adds a new export job to the queue
func CameraExportStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	nvr := findNVRByID(req.NVRID)
	if nvr == nil {
		http.Error(w, "NVR not found", http.StatusNotFound)
		return
	}

	if req.Quality == "" {
		req.Quality = "sub"
	}

	// Get requesting user
	username := getAuthUserID(r)
	if username == "" {
		username = "unknown"
	}

	jobUID := generateSessionID()
	job := &models.ExportJob{
		JobUID:      jobUID,
		NVRID:       req.NVRID,
		Channel:     req.Channel,
		CameraName:  req.CameraName,
		NVRName:     req.NVRName,
		StartTime:   req.StartTime,
		EndTime:     req.EndTime,
		Quality:     req.Quality,
		ExportName:  req.ExportName,
		Status:      "queued",
		RequestedBy: username,
		CreatedAt:   time.Now(),
	}

	if err := db.InsertExportJob(job); err != nil {
		exportLog("Camera Export: Failed to insert job: %v", err)
		http.Error(w, "Failed to queue export", http.StatusInternalServerError)
		return
	}

	exportLog("Camera Export: Queued job %s by %s (%s / %s, %s to %s)",
		jobUID, username, job.NVRName, job.CameraName, job.StartTime, job.EndTime)

	// Return response compatible with existing frontend (uses "id" field)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       jobUID,
		"jobUid":   jobUID,
		"status":   "queued",
		"progress": 0,
	})
}

// CameraExportProgressHandler returns the current progress of an export job
func CameraExportProgressHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cameras/export/progress/")
	jobUID := strings.TrimSuffix(path, "/")

	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		http.Error(w, "Export job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       job.JobUID,
		"jobUid":   job.JobUID,
		"status":   job.Status,
		"progress": job.Progress,
		"fileSize": job.FileSize,
		"fileName": job.FileName,
		"error":    job.Error,
	})
}

// CameraExportDownloadHandler serves the completed export file
func CameraExportDownloadHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/cameras/export/download/")
	jobUID := strings.TrimSuffix(path, "/")

	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		http.Error(w, "Export job not found", http.StatusNotFound)
		return
	}

	if job.Status != "complete" {
		http.Error(w, "Export not yet complete", http.StatusBadRequest)
		return
	}

	if job.FilePath == "" {
		http.Error(w, "Export file not found", http.StatusNotFound)
		return
	}

	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, job.FileName)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitized))
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, job.FilePath)
}

// CameraExportCancelHandler cancels a queued or running export
func CameraExportCancelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/cameras/export/cancel/")
	jobUID := strings.TrimSuffix(path, "/")

	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		http.Error(w, "Export job not found", http.StatusNotFound)
		return
	}

	// Update DB status
	db.UpdateExportJobStatus(jobUID, "cancelled", job.Progress, job.FileSize, "Cancelled by user")

	// Signal the worker to stop
	CancelExportJob(jobUID)

	// Clean up file if exists
	if job.FilePath != "" {
		os.Remove(job.FilePath)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"cancelled"}`))
}

// CameraExportDeleteHandler deletes a single completed/failed export (admin only)
func CameraExportDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/cameras/export/delete/")
	jobUID := strings.TrimSuffix(path, "/")

	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		http.Error(w, "Export job not found", http.StatusNotFound)
		return
	}

	// Only allow deleting completed/failed jobs
	if job.Status != "complete" && job.Status != "failed" {
		http.Error(w, "Can only delete completed or failed exports", http.StatusBadRequest)
		return
	}

	filePath, err := db.DeleteExportJob(jobUID)
	if err != nil {
		exportLog("Camera Export: Failed to delete job %s: %v", jobUID, err)
		http.Error(w, "Failed to delete export", http.StatusInternalServerError)
		return
	}

	if filePath != "" {
		os.Remove(filePath)
	}

	user := getAuthUserID(r)
	exportLog("Camera Export: Admin %s deleted export %s (%s/%s)", user, jobUID, job.NVRName, job.CameraName)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"deleted"}`))
}

// CameraExportPurgeHandler deletes all completed/failed exports (admin only)
func CameraExportPurgeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	paths, err := db.PurgeCompletedExportJobs()
	if err != nil {
		exportLog("Camera Export: Failed to purge jobs: %v", err)
		http.Error(w, "Failed to purge exports", http.StatusInternalServerError)
		return
	}

	for _, p := range paths {
		os.Remove(p)
	}

	user := getAuthUserID(r)
	exportLog("Camera Export: Admin %s purged %d completed/failed exports", user, len(paths))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "purged",
		"deleted": len(paths),
	})
}

// CameraExportRetryHandler re-queues a completed or failed export job (admin only)
func CameraExportRetryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin/exports/retry/")
	jobUID := strings.TrimSuffix(path, "/")

	job, err := db.GetExportJobByUID(jobUID)
	if err != nil || job == nil {
		http.Error(w, "Export job not found", http.StatusNotFound)
		return
	}

	if job.Status != "complete" && job.Status != "failed" {
		http.Error(w, "Can only retry completed or failed exports", http.StatusBadRequest)
		return
	}

	user := getAuthUserID(r)

	// Delete old OneDrive file if it has a share link
	if job.ShareLink != "" {
		od := exportConfig.Export.OneDrive
		if od != nil && od.TenantID != "" {
			if err := DeleteOneDriveFileByShareLink(od.TenantID, od.ClientID, od.ClientSecret, job.ShareLink); err != nil {
				exportLog("Camera Export: Retry — failed to delete old OneDrive file for %s: %v", jobUID, err)
			} else {
				exportLog("Camera Export: Retry — deleted old OneDrive file for %s", jobUID)
			}
		}
	}

	// Clean up old local file if exists
	if job.FilePath != "" {
		os.Remove(job.FilePath)
	}

	// Atomically reset all job fields back to queued (single DB call to avoid race with worker)
	if err := db.ResetExportJobForRetry(jobUID); err != nil {
		exportLog("Camera Export: Retry failed to reset job %s: %v", jobUID, err)
		http.Error(w, "Failed to reset job", http.StatusInternalServerError)
		return
	}

	exportLog("Camera Export: Admin %s retried export %s (%s/%s)", user, jobUID, job.NVRName, job.CameraName)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"queued"}`))
}

// CameraExportRestartWorkerHandler restarts the export worker goroutine (admin only)
func CameraExportRestartWorkerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := contextGetAuthenticatedUser(r)
	if !IsAdmin(user) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	RestartExportWorker()

	username := getAuthUserID(r)
	exportLog("Camera Export: Worker restarted by admin %s", username)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"restarted"}`))
}

// CameraExportDebugHandler returns debug info for the export system (admin only)
func CameraExportDebugHandler(w http.ResponseWriter, r *http.Request) {
	user := contextGetAuthenticatedUser(r)
	if !IsAdmin(user) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Optional job filter
	jobUID := r.URL.Query().Get("job")

	logs := GetExportDebugLogs(jobUID)

	// Get current worker config info
	exportWorkerMu.RLock()
	cfg := exportConfig
	exportWorkerMu.RUnlock()

	// Check active cancel channels
	cancelChansMu.Lock()
	activeChans := make([]string, 0, len(cancelChans))
	for uid := range cancelChans {
		activeChans = append(activeChans, uid)
	}
	cancelChansMu.Unlock()

	// Check export dir
	dirInfo := map[string]interface{}{
		"path":   cfg.Export.ExportDir,
		"exists": false,
	}
	if info, err := os.Stat(cfg.Export.ExportDir); err == nil {
		dirInfo["exists"] = true
		dirInfo["isDir"] = info.IsDir()
	}

	// Check ffmpeg/ffprobe availability
	_, ffmpegErr := exec.LookPath("ffmpeg")
	_, ffprobeErr := exec.LookPath("ffprobe")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"config": map[string]interface{}{
			"exportDir":      cfg.Export.ExportDir,
			"maxConcurrent":  cfg.Export.MaxConcurrent,
			"retentionHours": cfg.Export.RetentionHours,
			"encodeH264":     cfg.Export.EncodeH264,
		},
		"tools": map[string]interface{}{
			"ffmpegAvailable":  ffmpegErr == nil,
			"ffprobeAvailable": ffprobeErr == nil,
		},
		"exportDir":          dirInfo,
		"activeWorkerChans":  activeChans,
		"logs":               logs,
	})
}

// CameraExportListHandler returns the export list page or JSON for the current user
func CameraExportListHandler(w http.ResponseWriter, r *http.Request) {
	username := getAuthUserID(r)
	if username == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user := contextGetAuthenticatedUser(r)
	isAdmin := IsAdmin(user)

	// Admins see all jobs, regular users see only their own
	var jobs []models.ExportJob
	var err error
	if isAdmin {
		jobs, err = db.GetAllExportJobs(200)
	} else {
		jobs, err = db.GetExportJobsByUser(username, 50)
	}
	if err != nil {
		exportLog("Camera Export: Failed to get jobs for %s: %v", username, err)
		http.Error(w, "Failed to load exports", http.StatusInternalServerError)
		return
	}

	// Return JSON for AJAX requests
	if r.Header.Get("Accept") == "application/json" || r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jobs)
		return
	}

	// Render template
	data := struct {
		Jobs    []models.ExportJob
		User    string
		IsAdmin bool
		Version string
	}{
		Jobs:    jobs,
		User:    username,
		IsAdmin: isAdmin,
		Version: GetVersion(),
	}

	if err := templates.ExecuteTemplate(w, "camera_exports.html", data); err != nil {
		exportLog("Camera Export: Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
