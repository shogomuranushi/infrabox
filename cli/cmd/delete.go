package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		fmt.Printf("Deleting VM '%s'...\n", name)
		_, status, err := apiRequest("DELETE", "/v1/vms/"+name, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status == 404 {
			fmt.Fprintf(os.Stderr, "ERROR: VM '%s' not found\n", name)
			os.Exit(1)
		}
		if status != 204 {
			fmt.Fprintf(os.Stderr, "ERROR: status %d\n", status)
			os.Exit(1)
		}
		fmt.Printf("✓ '%s' deleted\n", name)
	},
}
