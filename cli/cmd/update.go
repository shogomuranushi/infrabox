package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	githubRepo      = "shogomuranushi/infrabox"
	updateCheckFile = ".ib/last_update_check"
	updateInterval  = 24 * time.Hour
)

type githubRelease struct {
	TagName string `json:"tag_name"`
}

// checkUpdateBackground runs an update check in the background at startup.
// If a newer version is available, it downloads and replaces the binary automatically.
// Skipped if a check was performed within the last 24 hours.
func checkUpdateBackground() {
	if !shouldCheck() {
		return
	}
	go func() {
		latest, err := fetchLatestVersion()
		if err != nil {
			return
		}
		saveCheckTime()
		if !isNewer(latest, Version) {
			return
		}
		binPath, err := writableBinPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n[ib] New version available: %s → %s (run 'ib upgrade' to update)\n", Version, latest)
			return
		}
		if err := downloadAndReplace(latest, binPath); err != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "\n[ib] Auto-updated to %s\n", latest)
	}()
}

// writableBinPath returns a path where the ib binary can be written without sudo.
// Prefers ~/.local/bin/ib if it exists (set up by ib init), then falls back to
// the current executable location if writable, then ~/.local/bin/ib as a last resort.
func writableBinPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	localIb := filepath.Join(home, ".local", "bin", "ib")

	// Prefer ~/.local/bin/ib if already set up
	if _, err := os.Stat(localIb); err == nil {
		return localIb, nil
	}

	// Try current executable location
	if exe, err := os.Executable(); err == nil {
		if f, err := os.OpenFile(exe, os.O_WRONLY, 0); err == nil {
			f.Close()
			return exe, nil
		}
	}

	// Fall back to ~/.local/bin/ib
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return localIb, nil
}

func shouldCheck() bool {
	path := checkFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}
	return time.Since(t) > updateInterval
}

func saveCheckTime() {
	path := checkFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0600)
}

func fetchLatestVersion() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API status: %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// isNewer reports whether latest is a newer version than current.
// Strips leading "v" before comparing so "v0.2.7" == "0.2.7".
func isNewer(latest, current string) bool {
	if latest == "" {
		return false
	}
	l := strings.TrimPrefix(latest, "v")
	c := strings.TrimPrefix(current, "v")
	return l != c
}

func checkFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, updateCheckFile)
}
