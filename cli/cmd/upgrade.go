package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade ib to the latest version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Checking for updates...")
		latest, err := fetchLatestVersion()
		if err != nil {
			return fmt.Errorf("failed to fetch latest version: %w", err)
		}

		if !isNewer(latest, Version) {
			fmt.Printf("Already up to date (%s)\n", Version)
			return nil
		}

		fmt.Printf("Updating %s → %s\n", Version, latest)

		binPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot determine executable path: %w", err)
		}

		if err := downloadAndReplace(latest, binPath); err != nil {
			return fmt.Errorf("upgrade failed: %w", err)
		}

		saveCheckTime()
		fmt.Printf("Successfully updated to %s\n", latest)
		return nil
	},
}

func downloadAndReplace(version, binPath string) error {
	url := assetURL(version)
	fmt.Printf("Downloading %s\n", url)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// extract ib binary from tar.gz
	newBin, err := extractBinary(resp.Body)
	if err != nil {
		return err
	}

	// write to temp file then atomically replace the existing binary
	tmp := binPath + ".tmp"
	if err := os.WriteFile(tmp, newBin, 0755); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied — try: sudo ib upgrade")
		}
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, binPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func extractBinary(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == "ib" || hdr.Name == "ib.exe" {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary not found in archive")
}

func assetURL(version string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf(
		"https://github.com/%s/releases/download/%s/ib_%s_%s.%s",
		githubRepo, version, goos, goarch, ext,
	)
}
