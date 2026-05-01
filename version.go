package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Version is set at build time via -ldflags or read from VERSION file
// Example: go build -ldflags "-X main.Version=v1.0.0"
var Version string

// BuildTime is set at build time via -ldflags
var BuildTime string

// versionFilePath is the path to the VERSION file
var versionFilePath string

// currentVersion holds the currently running version
var currentVersion string

// GetVersion returns the current application version
// Priority: VERSION file > Version flag > git commit hash > build timestamp
func GetVersion() string {
	// Try to read from VERSION file first
	if v := readVersionFile(); v != "" {
		return v
	}

	if Version != "" {
		return Version
	}

	// Try to get git commit hash
	if hash := getGitHash(); hash != "" {
		return hash
	}

	// Fallback to build time if set
	if BuildTime != "" {
		return BuildTime
	}

	// Last resort: use current time (will cause reload on every restart)
	return time.Now().Format("20060102150405")
}

// readVersionFile reads the version from the VERSION file
func readVersionFile() string {
	if versionFilePath == "" {
		// Try to find VERSION file in current directory or executable directory
		if _, err := os.Stat("VERSION"); err == nil {
			versionFilePath = "VERSION"
		} else {
			execPath, err := os.Executable()
			if err == nil {
				versionFilePath = filepath.Join(filepath.Dir(execPath), "VERSION")
			}
		}
	}

	if versionFilePath == "" {
		return ""
	}

	data, err := os.ReadFile(versionFilePath)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// getGitHash attempts to get the current git commit short hash
func getGitHash() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// StartVersionWatcher starts a goroutine that watches the VERSION file for changes
// and restarts the application when the version changes
func StartVersionWatcher() {
	currentVersion = GetVersion()
	log.Printf("Version watcher started (current: %s)", currentVersion)

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			newVersion := readVersionFile()
			if newVersion != "" && newVersion != currentVersion {
				log.Printf("Version changed: %s -> %s, restarting...", currentVersion, newVersion)
				restartApplication()
			}
		}
	}()
}

// restartApplication restarts the current application
func restartApplication() {
	executable, err := os.Executable()
	if err != nil {
		log.Printf("Failed to get executable path: %v", err)
		return
	}

	// Get the absolute path
	executable, err = filepath.Abs(executable)
	if err != nil {
		log.Printf("Failed to get absolute path: %v", err)
		return
	}

	log.Printf("Restarting: %s", executable)

	// Use syscall.Exec to replace the current process
	err = syscall.Exec(executable, os.Args, os.Environ())
	if err != nil {
		log.Printf("Failed to restart: %v", err)
	}
}
