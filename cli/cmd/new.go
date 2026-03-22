package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		fmt.Printf("Creating VM '%s'...\n", name)
		start := time.Now()

		data, status, err := apiRequest("POST", "/v1/vms", map[string]string{
			"name": name,
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

		elapsed := int(time.Since(start).Seconds())
		fmt.Printf("\nReady (%ds)\n\n", elapsed)
		fmt.Printf("  Shell:     ib ssh %s\n", vm.Name)
		fmt.Printf("  HTTPS URL: %s\n\n", vm.IngressURL)
	},
}
