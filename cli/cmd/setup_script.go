package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var setupScriptCmd = &cobra.Command{
	Use:   "setup-script",
	Short: "Manage your VM setup script",
}

var setupScriptSetCmd = &cobra.Command{
	Use:   "set <file>",
	Short: "Register a setup script from a file (runs on new VM creation)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		path := args[0]

		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: cannot read file: %v\n", err)
			os.Exit(1)
		}
		if len(content) == 0 {
			fmt.Fprintln(os.Stderr, "ERROR: file is empty")
			os.Exit(1)
		}

		data, status, err := apiRequest("PUT", "/v1/setup-script", map[string]string{
			"script": string(content),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}
		fmt.Println("Setup script saved. It will run automatically when you create a new VM.")
	},
}

var setupScriptShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show your current setup script",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()

		data, status, err := apiRequest("GET", "/v1/setup-script", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status == 404 {
			fmt.Println("No setup script configured.")
			return
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}

		var resp struct {
			Script string `json:"script"`
		}
		json.Unmarshal(data, &resp)
		fmt.Print(resp.Script)
	},
}

var setupScriptDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete your setup script",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()

		data, status, err := apiRequest("DELETE", "/v1/setup-script", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 204 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}
		fmt.Println("Setup script deleted.")
	},
}

func init() {
	setupScriptCmd.AddCommand(setupScriptSetCmd, setupScriptShowCmd, setupScriptDeleteCmd)
}
