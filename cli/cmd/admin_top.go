package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type AdminResourcesResp struct {
	Nodes      []NodeResourceResp      `json:"nodes"`
	Namespaces []NamespaceResourceResp `json:"namespaces"`
	Totals     ClusterTotals           `json:"totals"`
}

type NodeResourceResp struct {
	Name   string           `json:"name"`
	CPU    NodeResourceItem `json:"cpu"`
	Memory NodeResourceItem `json:"memory"`
}

type NodeResourceItem struct {
	Allocatable int64 `json:"allocatable"`
	Requests    int64 `json:"requests"`
}

type NamespaceResourceResp struct {
	Namespace string        `json:"namespace"`
	Owner     string        `json:"owner"`
	VMCount   int           `json:"vm_count"`
	CPU       ResourceUsage `json:"cpu"`
	Memory    ResourceUsage `json:"memory"`
}

type ClusterTotals struct {
	Nodes       int   `json:"nodes"`
	TotalCPU    int64 `json:"total_cpu"`
	UsedCPU     int64 `json:"used_cpu"`
	TotalMemory int64 `json:"total_memory"`
	UsedMemory  int64 `json:"used_memory"`
	TotalVMs    int   `json:"total_vms"`
}

var adminTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Show cluster-wide resource usage (admin only)",
	Run: func(cmd *cobra.Command, args []string) {
		mustAdminConfig()
		data, status, err := doRequest("GET", "/v1/admin/resources", nil, cfg.AdminKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}
		var resp AdminResourcesResp
		if err := json.Unmarshal(data, &resp); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to parse response\n")
			os.Exit(1)
		}
		renderAdminTop(resp)
	},
}

func renderAdminTop(r AdminResourcesResp) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                     InfraBox Cluster Status                      ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════╣")
	fmt.Println()

	// --- Nodes ---
	fmt.Printf("  Nodes (%d)\n", r.Totals.Nodes)
	fmt.Println("  " + strings.Repeat("─", 62))

	// Find max node name length for alignment
	maxName := 8
	for _, n := range r.Nodes {
		if len(n.Name) > maxName {
			maxName = len(n.Name)
		}
	}
	if maxName > 24 {
		maxName = 24
	}

	barW := 20
	for _, n := range r.Nodes {
		name := n.Name
		if len(name) > maxName {
			name = name[:maxName]
		}
		fmt.Printf("  %-*s  CPU %s  MEM %s\n",
			maxName, name,
			renderBar(n.CPU.Requests, n.CPU.Allocatable, barW),
			renderBar(n.Memory.Requests, n.Memory.Allocatable, barW))
	}

	fmt.Println("  " + strings.Repeat("─", 62))
	fmt.Printf("  %-*s  CPU %s  MEM %s\n",
		maxName, "Total",
		renderBar(r.Totals.UsedCPU, r.Totals.TotalCPU, barW),
		renderBar(r.Totals.UsedMemory, r.Totals.TotalMemory, barW))
	fmt.Printf("  %*s      %s / %s              %s / %s\n",
		maxName, "",
		formatCPU(r.Totals.UsedCPU), formatCPU(r.Totals.TotalCPU),
		formatMemory(r.Totals.UsedMemory), formatMemory(r.Totals.TotalMemory))
	fmt.Println()

	// --- Users ---
	totalUsers := len(r.Namespaces)
	fmt.Printf("  Users (%d active, %d VMs)\n", totalUsers, r.Totals.TotalVMs)
	fmt.Println("  " + strings.Repeat("─", 62))

	// Find max owner name length
	maxOwner := 4
	for _, ns := range r.Namespaces {
		if len(ns.Owner) > maxOwner {
			maxOwner = len(ns.Owner)
		}
	}
	if maxOwner > 28 {
		maxOwner = 28
	}

	fmt.Printf("  %-*s  %3s  %8s  %8s  %s\n", maxOwner, "USER", "VMs", "CPU(req)", "MEM(req)", "QUOTA USAGE")
	for _, ns := range r.Namespaces {
		owner := ns.Owner
		if len(owner) > maxOwner {
			owner = owner[:maxOwner]
		}

		// Use CPU quota for the bar if available, otherwise skip bar
		quotaBar := ""
		if ns.CPU.Quota > 0 {
			quotaBar = renderBar(ns.CPU.Used, ns.CPU.Quota, 20)
		}

		fmt.Printf("  %-*s  %3d  %8s  %8s  %s\n",
			maxOwner, owner,
			ns.VMCount,
			formatCPU(ns.CPU.Used),
			formatMemory(ns.Memory.Used),
			quotaBar)
	}

	fmt.Println("  " + strings.Repeat("─", 62))
	fmt.Println()
	fmt.Println("╚═══════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func init() {
	adminCmd.AddCommand(adminTopCmd)
}
