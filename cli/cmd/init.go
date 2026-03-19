package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Set up API key for first-time use",
	Run: func(cmd *cobra.Command, args []string) {
		if cfg.Endpoint == "" {
			fmt.Fprintln(os.Stderr, "ERROR: endpoint is not configured")
			os.Exit(1)
		}

		keysURL := strings.TrimRight(cfg.Endpoint, "/") + "/v1/keys"

		fmt.Printf("\nGet your API key:\n\n")
		fmt.Printf("  1. Open in browser: %s\n", keysURL)
		fmt.Printf("  2. Sign in with Google (if prompted)\n")
		fmt.Printf("  3. Copy the \"api_key\" value from the response\n\n")
		fmt.Print("Paste your API key: ")

		reader := bufio.NewReader(os.Stdin)
		key, _ := reader.ReadString('\n')
		key = strings.TrimSpace(key)

		// Accept raw key or JSON {"api_key":"..."}
		if strings.HasPrefix(key, "{") {
			var resp struct {
				APIKey string `json:"api_key"`
			}
			if err := json.Unmarshal([]byte(key), &resp); err == nil && resp.APIKey != "" {
				key = resp.APIKey
			}
		}

		if key == "" {
			fmt.Fprintln(os.Stderr, "ERROR: API key is required")
			os.Exit(1)
		}

		cfg.APIKey = key
		if err := saveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to save config: %v\n", err)
			os.Exit(1)
		}

		if err := ensureSSHKey(); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to generate SSH key: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\n✓ Setup complete\n\n")
		fmt.Printf("  Run 'ib new <name>' to create a VM\n\n")
	},
}
