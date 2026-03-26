package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// SyncEntry represents a single file/directory to transfer on VM creation.
// Dst can be either a directory path (e.g. "/home/ubuntu/.claude/") or a
// full file path (e.g. "/home/ubuntu/.claude.json"). When Dst does not end
// with "/" and Src is a file, the parent directory of Dst is used as the
// extraction target so the file lands at the expected path.
type SyncEntry struct {
	Src string `yaml:"src"` // local path (~ expanded at runtime)
	Dst string `yaml:"dst"` // destination path on the VM (file or directory)
}

// SyncConfig holds the list of sync entries persisted in ~/.ib/sync.yaml.
type SyncConfig struct {
	Files []SyncEntry `yaml:"files"`
}

func syncConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ib", "sync.yaml")
}

func loadSyncConfig() (SyncConfig, error) {
	var sc SyncConfig
	data, err := os.ReadFile(syncConfigPath())
	if os.IsNotExist(err) {
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	return sc, yaml.Unmarshal(data, &sc)
}

func saveSyncConfig(sc SyncConfig) error {
	path := syncConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(sc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// applySync uploads all sync entries to the VM. Errors are printed as warnings
// so that VM creation is not aborted by a missing or unreadable file.
func applySync(vmName string) {
	sc, err := loadSyncConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: failed to load sync config: %v\n", err)
		return
	}
	if len(sc.Files) == 0 {
		return
	}
	fmt.Printf("Syncing %d file(s) to '%s'...\n", len(sc.Files), vmName)
	for _, entry := range sc.Files {
		src := expandHome(entry.Src)
		info, err := os.Stat(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARNING: skipping %s: %v\n", entry.Src, err)
			continue
		}
		// If dst is a file path (no trailing "/") and src is a file, use the
		// parent directory so the file lands at the exact destination path.
		destDir := entry.Dst
		if !info.IsDir() && !strings.HasSuffix(destDir, "/") {
			destDir = filepath.Dir(destDir) + "/"
		}
		if err := uploadToVM(vmName, destDir, src, info.IsDir()); err != nil {
			fmt.Fprintf(os.Stderr, "  WARNING: failed to sync %s: %v\n", entry.Src, err)
		} else {
			fmt.Printf("  synced %s -> %s\n", entry.Src, entry.Dst)
		}
	}
}

// --- commands ---

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manage files to auto-sync on VM creation",
}

var syncAddCmd = &cobra.Command{
	Use:   "add <src> <dst>",
	Short: "Add a local file/directory to the sync list",
	Long: `Add a local file or directory to be automatically transferred to every new VM.

Examples:
  ib sync add ~/.claude/settings.json /home/ubuntu/.claude/
  ib sync add ~/.config/somemcp       /home/ubuntu/.config/`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		sc, err := loadSyncConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		entry := SyncEntry{Src: args[0], Dst: args[1]}
		for _, e := range sc.Files {
			if e.Src == entry.Src {
				fmt.Fprintf(os.Stderr, "ERROR: %s is already in the sync list\n", entry.Src)
				os.Exit(1)
			}
		}
		sc.Files = append(sc.Files, entry)
		if err := saveSyncConfig(sc); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Added: %s -> %s\n", entry.Src, entry.Dst)
	},
}

var syncListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sync entries",
	Run: func(cmd *cobra.Command, args []string) {
		sc, err := loadSyncConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if len(sc.Files) == 0 {
			fmt.Println("No sync entries. Use 'ib sync add <src> <dst>' to add one.")
			return
		}
		for _, e := range sc.Files {
			fmt.Printf("  %s -> %s\n", e.Src, e.Dst)
		}
	},
}

var syncRemoveCmd = &cobra.Command{
	Use:   "remove <src>",
	Short: "Remove a sync entry by its src path",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sc, err := loadSyncConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		before := len(sc.Files)
		var filtered []SyncEntry
		for _, e := range sc.Files {
			if e.Src != args[0] {
				filtered = append(filtered, e)
			}
		}
		if len(filtered) == before {
			fmt.Fprintf(os.Stderr, "ERROR: %s not found in sync list\n", args[0])
			os.Exit(1)
		}
		sc.Files = filtered
		if err := saveSyncConfig(sc); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Removed: %s\n", args[0])
	},
}

var syncNowCmd = &cobra.Command{
	Use:   "now <vmname>",
	Short: "Sync files to an existing VM immediately",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		applySync(args[0])
	},
}

func init() {
	syncCmd.AddCommand(syncAddCmd, syncListCmd, syncRemoveCmd, syncNowCmd)
}
