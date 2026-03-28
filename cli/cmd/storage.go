package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type StorageInfoResponse struct {
	Capacity   string      `json:"capacity"`
	TotalBytes int64       `json:"total_bytes"`
	UsedBytes  int64       `json:"used_bytes"`
	AvailBytes int64       `json:"avail_bytes"`
	UsePercent string      `json:"use_percent"`
	Files      string      `json:"files,omitempty"`
	Error      string      `json:"error,omitempty"`
}

var storageCmd = &cobra.Command{
	Use:   "storage",
	Short: "Manage VM persistent storage",
	Long: `View storage usage and browse files on VM persistent volumes.

The persistent volume (/home/ubuntu) survives VM restarts.
Data is preserved as long as the VM exists.`,
}

var storageUsageCmd = &cobra.Command{
	Use:   "usage <vmname>",
	Short: "Show storage usage for a VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		vmName := args[0]

		data, status, err := apiRequest("GET", fmt.Sprintf("/v1/vms/%s/storage", vmName), nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}

		var info StorageInfoResponse
		json.Unmarshal(data, &info)

		fmt.Printf("Storage for '%s':\n\n", vmName)
		fmt.Printf("  Capacity:   %s\n", info.Capacity)
		fmt.Printf("  Used:       %s (%s)\n", formatBytes(info.UsedBytes), info.UsePercent)
		fmt.Printf("  Available:  %s\n", formatBytes(info.AvailBytes))
		fmt.Printf("  Total:      %s\n", formatBytes(info.TotalBytes))
		fmt.Println()
		fmt.Printf("  Mount path: /home/ubuntu\n")
		fmt.Printf("  Lifecycle:  persists across restarts, deleted with VM\n")
	},
}

var storageLsCmd = &cobra.Command{
	Use:   "ls <vmname> [path]",
	Short: "List files on VM storage",
	Long: `List files on a VM's persistent storage.
Default path is /home/ubuntu.

Examples:
  ib storage ls myvm
  ib storage ls myvm /home/ubuntu/.config`,
	Args: cobra.RangeArgs(1, 2),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		vmName := args[0]
		path := "/home/ubuntu"
		if len(args) > 1 {
			path = args[1]
		}

		url := fmt.Sprintf("/v1/vms/%s/storage?action=ls&path=%s", vmName, path)
		data, status, err := apiRequest("GET", url, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}

		var info StorageInfoResponse
		json.Unmarshal(data, &info)

		fmt.Printf("Files in %s on '%s':\n\n", path, vmName)
		if info.Files != "" {
			fmt.Print(info.Files)
		} else {
			fmt.Println("  (empty)")
		}
	},
}

func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func init() {
	storageCmd.AddCommand(storageUsageCmd, storageLsCmd)
}
