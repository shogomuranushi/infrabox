package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		_, status, err := apiRequest("POST", "/v1/vms/"+name+"/restart", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status == 404 {
			fmt.Fprintf(os.Stderr, "ERROR: VM '%s' not found\n", name)
			os.Exit(1)
		}
		fmt.Printf("✓ '%s' restarted\n", name)
	},
}
