package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]
		storage, _ := cmd.Flags().GetString("storage")

		fmt.Printf("Creating VM '%s'...\n", name)
		if storage != "" && storage != "8Gi" {
			fmt.Printf("  Storage: %s\n", storage)
		}
		start := time.Now()

		body := map[string]string{"name": name}
		if storage != "" {
			body["storage"] = storage
		}

		data, status, err := apiRequest("POST", "/v1/vms", body)
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
		if vm.State == "starting" {
			fmt.Printf("\nVM '%s' created — pod still starting (%ds). SSH will be ready in a moment.\n\n", vm.Name, elapsed)
		} else {
			fmt.Printf("\nReady (%ds)\n\n", elapsed)
		}
		fmt.Printf("  Shell:     ib ssh %s\n", vm.Name)
		fmt.Printf("  HTTPS URL: %s\n\n", vm.IngressURL)

		applySync(name)
	},
}

func init() {
	createCmd.Flags().String("storage", "", "Storage size (e.g. 8Gi, 16Gi, 32Gi). Default: 8Gi")
}
