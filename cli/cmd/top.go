package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type UserResourcesResp struct {
	VMCount int           `json:"vm_count"`
	CPU     ResourceUsage `json:"cpu"`
	Memory  ResourceUsage `json:"memory"`
}

type ResourceUsage struct {
	Used  int64  `json:"used"`
	Quota int64  `json:"quota"`
	Unit  string `json:"unit"`
}

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Show your resource usage",
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		data, status, err := apiRequest("GET", "/v1/resources", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			var errResp struct{ Error string `json:"error"` }
			json.Unmarshal(data, &errResp)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", errResp.Error)
			os.Exit(1)
		}
		var resp UserResourcesResp
		if err := json.Unmarshal(data, &resp); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to parse response\n")
			os.Exit(1)
		}
		renderUserTop(resp)
	},
}

func renderUserTop(r UserResourcesResp) {
	fmt.Println()
	fmt.Println("  Resource Usage")
	fmt.Println("  " + strings.Repeat("═", 56))
	fmt.Printf("  VMs: %d\n", r.VMCount)
	fmt.Println()

	cpuUsed := formatCPU(r.CPU.Used)
	cpuQuota := formatCPU(r.CPU.Quota)
	memUsed := formatMemory(r.Memory.Used)
	memQuota := formatMemory(r.Memory.Quota)

	fmt.Printf("  CPU    %s  %s / %s\n", renderBar(r.CPU.Used, r.CPU.Quota, 30), cpuUsed, cpuQuota)
	fmt.Printf("  Memory %s  %s / %s\n", renderBar(r.Memory.Used, r.Memory.Quota, 30), memUsed, memQuota)
	fmt.Println()
}

// --- Shared bar chart utilities ---

func renderBar(used, total int64, width int) string {
	if total == 0 {
		return "[" + strings.Repeat("░", width) + "]   -%"
	}
	pct := float64(used) / float64(total)
	if pct > 1.0 {
		pct = 1.0
	}
	filled := int(math.Round(pct * float64(width)))
	if filled > width {
		filled = width
	}
	empty := width - filled
	return fmt.Sprintf("[%s%s] %3d%%",
		strings.Repeat("█", filled),
		strings.Repeat("░", empty),
		int(pct*100))
}

func formatCPU(millicores int64) string {
	if millicores <= 0 {
		return "0"
	}
	if millicores < 1000 {
		return fmt.Sprintf("%dm", millicores)
	}
	cores := float64(millicores) / 1000.0
	if cores == float64(int64(cores)) {
		return fmt.Sprintf("%d", int64(cores))
	}
	return fmt.Sprintf("%.1f", cores)
}

func formatMemory(bytes int64) string {
	if bytes <= 0 {
		return "0"
	}
	const gi = 1024 * 1024 * 1024
	const mi = 1024 * 1024
	if bytes >= gi {
		v := float64(bytes) / float64(gi)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%dGi", int64(v))
		}
		return fmt.Sprintf("%.1fGi", v)
	}
	return fmt.Sprintf("%dMi", bytes/mi)
}
