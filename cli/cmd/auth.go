package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage VM authentication",
}

var authEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Enable oauth2 auth on a VM's HTTPS endpoint",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runAuthUpdate(args[0], true)
	},
}

var authDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Disable oauth2 auth on a VM's HTTPS endpoint (fully open)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runAuthUpdate(args[0], false)
	},
}

func runAuthUpdate(name string, enabled bool) {
	mustConfig()

	data, status, err := apiRequest("PATCH", "/v1/vms/"+name+"/auth", map[string]bool{
		"enabled": enabled,
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

	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Auth %s for '%s'\n", state, vm.Name)
	fmt.Printf("  URL: %s\n", vm.IngressURL)
}

func init() {
	authCmd.AddCommand(authEnableCmd, authDisableCmd)
}
