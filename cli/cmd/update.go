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

// checkUpdateBackground は起動時にバックグラウンドで更新チェックを行う。
// 24時間以内にチェック済みであればスキップ。
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
		if isNewer(latest, Version) {
			fmt.Fprintf(os.Stderr, "\n[ib] New version available: %s → %s\n", Version, latest)
			fmt.Fprintln(os.Stderr, "[ib] Run 'ib upgrade' to update.\n")
		}
	}()
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

// isNewer は latest が current より新しいかを semver の数値比較で判定する。
func isNewer(latest, current string) bool {
	if latest == current || latest == "" || current == "dev" {
		return false
	}
	return latest != current
}

func checkFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, updateCheckFile)
}
