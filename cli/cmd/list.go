package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Short:   "List VMs",
	Aliases: []string{"ls"},
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()

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

		fmt.Printf("%-20s %-10s %-6s %s\n", "NAME", "STATE", "AUTH", "URL")
		fmt.Printf("%-20s %-10s %-6s %s\n", "----", "-----", "----", "---")
		for _, vm := range resp.VMs {
			auth := "on"
			if !vm.AuthEnabled {
				auth = "off"
			}
			fmt.Printf("%-20s %-10s %-6s %s\n", vm.Name, vm.State, auth, vm.IngressURL)
		}
	},
}
