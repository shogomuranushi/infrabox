package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List VMs",
	Aliases: []string{"ls"},
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		showStorage, _ := cmd.Flags().GetBool("storage")

		data, status, err := apiRequest("GET", "/v1/vms", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: status %d\n", status)
			os.Exit(1)
		}

		var resp VMListResponse
		json.Unmarshal(data, &resp)

		if len(resp.VMs) == 0 {
			fmt.Println("No VMs found. Run `ib new <name>` to create one.")
			return
		}

		if showStorage {
			// Fetch storage info for each running VM in parallel
			type storageResult struct {
				idx  int
				info StorageInfoResponse
			}
			results := make([]StorageInfoResponse, len(resp.VMs))
			var wg sync.WaitGroup
			for i, vm := range resp.VMs {
				if vm.State != "running" {
					continue
				}
				wg.Add(1)
				go func(idx int, vmName string) {
					defer wg.Done()
					d, s, err := apiRequest("GET", fmt.Sprintf("/v1/vms/%s/storage", vmName), nil)
					if err != nil || s != 200 {
						return
					}
					var info StorageInfoResponse
					json.Unmarshal(d, &info)
					results[idx] = info
				}(i, vm.Name)
			}
			wg.Wait()

			fmt.Printf("%-20s %-10s %-6s %-12s %-8s %s\n", "NAME", "STATE", "AUTH", "STORAGE", "USED", "URL")
			fmt.Printf("%-20s %-10s %-6s %-12s %-8s %s\n", "----", "-----", "----", "-------", "----", "---")
			for i, vm := range resp.VMs {
				auth := "on"
				if !vm.AuthEnabled {
					auth = "off"
				}
				storage := "-"
				used := "-"
				if results[i].Capacity != "" {
					storage = results[i].Capacity
					used = results[i].UsePercent
				}
				fmt.Printf("%-20s %-10s %-6s %-12s %-8s %s\n", vm.Name, vm.State, auth, storage, used, vm.IngressURL)
			}
		} else {
			fmt.Printf("%-20s %-10s %-6s %s\n", "NAME", "STATE", "AUTH", "URL")
			fmt.Printf("%-20s %-10s %-6s %s\n", "----", "-----", "----", "---")
			for _, vm := range resp.VMs {
				auth := "on"
				if !vm.AuthEnabled {
					auth = "off"
				}
				fmt.Printf("%-20s %-10s %-6s %s\n", vm.Name, vm.State, auth, vm.IngressURL)
			}
			fmt.Printf("\nTip: Use 'ib list --storage' to show disk usage, or 'ib storage usage <vm>' for details.\n")
		}
	},
}

func init() {
	listCmd.Flags().BoolP("storage", "s", false, "Show storage usage for each VM")
}
