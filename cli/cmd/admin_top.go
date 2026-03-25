package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorDim    = "\033[2m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
)

type AdminResourcesResp struct {
	Nodes      []NodeResourceResp      `json:"nodes"`
	Namespaces []NamespaceResourceResp `json:"namespaces"`
	Totals     ClusterTotals           `json:"totals"`
}

type NodeResourceResp struct {
	Name   string           `json:"name"`
	Role   string           `json:"role"`
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
	// Split nodes by role
	var vmNodes, sysNodes []NodeResourceResp
	for _, n := range r.Nodes {
		if n.Role == "vm-worker" {
			vmNodes = append(vmNodes, n)
		} else {
			sysNodes = append(sysNodes, n)
		}
	}

	// Totals for vm-worker nodes only
	var vmCPUAlloc, vmCPUReq, vmMemAlloc, vmMemReq int64
	for _, n := range vmNodes {
		vmCPUAlloc += n.CPU.Allocatable
		vmCPUReq += n.CPU.Requests
		vmMemAlloc += n.Memory.Allocatable
		vmMemReq += n.Memory.Requests
	}

	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                     InfraBox Cluster Status                      ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════╣")
	fmt.Println()

	// --- VM Worker Nodes ---
	fmt.Printf("  %sVM Worker Nodes (%d)%s\n", colorCyan, len(vmNodes), colorReset)
	fmt.Println("  " + strings.Repeat("─", 62))

	maxName := 8
	for _, n := range vmNodes {
		if len(n.Name) > maxName {
			maxName = len(n.Name)
		}
	}
	if maxName > 24 {
		maxName = 24
	}

	barW := 20
	for _, n := range vmNodes {
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
		renderBar(vmCPUReq, vmCPUAlloc, barW),
		renderBar(vmMemReq, vmMemAlloc, barW))
	fmt.Printf("  %*s      %s / %s              %s / %s\n",
		maxName, "",
		formatCPU(vmCPUReq), formatCPU(vmCPUAlloc),
		formatMemory(vmMemReq), formatMemory(vmMemAlloc))
	fmt.Println()

	// --- System Nodes (summary only) ---
	if len(sysNodes) > 0 {
		var sCPUAlloc, sCPUReq, sMemAlloc, sMemReq int64
		for _, n := range sysNodes {
			sCPUAlloc += n.CPU.Allocatable
			sCPUReq += n.CPU.Requests
			sMemAlloc += n.Memory.Allocatable
			sMemReq += n.Memory.Requests
		}
		fmt.Printf("  %sSystem Nodes (%d)  CPU %s  MEM %s%s\n",
			colorDim, len(sysNodes),
			renderBar(sCPUReq, sCPUAlloc, barW),
			renderBar(sMemReq, sMemAlloc, barW),
			colorReset)
		fmt.Println()
	}

	// --- Users ---
	userCount := 0
	for _, ns := range r.Namespaces {
		if ns.Owner != "(admin)" {
			userCount++
		}
	}
	fmt.Printf("  Users (%d active, %d VMs)\n", userCount, r.Totals.TotalVMs)
	fmt.Println("  " + strings.Repeat("─", 62))

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

		quotaBar := ""
		if ns.CPU.Quota > 0 {
			quotaBar = renderBar(ns.CPU.Used, ns.CPU.Quota, 20)
		}

		if ns.Owner == "(admin)" {
			fmt.Printf("  %s%-*s  %3d  %8s  %8s  %s%s\n",
				colorDim, maxOwner, owner,
				ns.VMCount,
				formatCPU(ns.CPU.Used),
				formatMemory(ns.Memory.Used),
				quotaBar, colorReset)
		} else {
			fmt.Printf("  %-*s  %3d  %8s  %8s  %s\n",
				maxOwner, owner,
				ns.VMCount,
				formatCPU(ns.CPU.Used),
				formatMemory(ns.Memory.Used),
				quotaBar)
		}
	}

	fmt.Println("  " + strings.Repeat("─", 62))
	fmt.Println()
	fmt.Println("╚═══════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func init() {
	adminCmd.AddCommand(adminTopCmd)
}
