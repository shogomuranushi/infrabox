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
	Short: "Set up CLI with endpoint and API key",
	Run: func(cmd *cobra.Command, args []string) {
		reader := bufio.NewReader(os.Stdin)

		// Endpoint
		defaultEP := cfg.Endpoint
		if defaultEP == "" {
			defaultEP = defaultEndpoint
		}
		if defaultEP != "" {
			fmt.Printf("Endpoint [%s]: ", defaultEP)
		} else {
			fmt.Print("Endpoint (e.g. https://api.infrabox.example.com): ")
		}
		ep, _ := reader.ReadString('\n')
		ep = strings.TrimSpace(ep)
		if ep == "" {
			ep = defaultEP
		}
		if ep == "" {
			fmt.Fprintln(os.Stderr, "ERROR: endpoint is required")
			os.Exit(1)
		}
		cfg.Endpoint = ep

		// Name
		fmt.Print("Name (e.g. your email): ")
		name, _ := reader.ReadString('\n')
		name = strings.TrimSpace(name)
		if name == "" {
			fmt.Fprintln(os.Stderr, "ERROR: name is required")
			os.Exit(1)
		}

		// Invitation code
		fmt.Print("Invitation code: ")
		code, _ := reader.ReadString('\n')
		code = strings.TrimSpace(code)
		if code == "" {
			fmt.Fprintln(os.Stderr, "ERROR: invitation code is required")
			os.Exit(1)
		}

		// POST /v1/keys to obtain API key
		data, status, err := doRequest("POST", "/v1/keys", map[string]string{
			"name":            name,
			"invitation_code": code,
		}, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			var errResp struct {
				Error string `json:"error"`
			}
			json.Unmarshal(data, &errResp)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", errResp.Error)
			os.Exit(1)
		}

		var resp struct {
			APIKey string `json:"api_key"`
		}
		if err := json.Unmarshal(data, &resp); err != nil || resp.APIKey == "" {
			fmt.Fprintln(os.Stderr, "ERROR: unexpected response from server")
			os.Exit(1)
		}

		cfg.APIKey = resp.APIKey
		if err := saveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to save config: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("\n✓ Setup complete. Run 'ib new <name>' to create a VM.\n\n")
	},
}
