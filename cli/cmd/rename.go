package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var renameCmd = &cobra.Command{
	Use:   "rename <old-name> <new-name>",
	Short: "Rename a VM",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		oldName := args[0]
		newName := args[1]

		data, status, err := apiRequest("PATCH", "/v1/vms/"+oldName, map[string]string{
			"name": newName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

		var vm VMResponse
		json.Unmarshal(data, &vm)

		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", vm.Error)
			os.Exit(1)
		}

		fmt.Printf("✓ '%s' renamed to '%s'\n", oldName, newName)
	},
}
