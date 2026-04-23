package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

		// Skip registration if API key is already configured
		if cfg.APIKey == "" {
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
		}

		if err := saveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to save config: %v\n", err)
			os.Exit(1)
		}

		setupLocalBin()
		fmt.Printf("\n✓ Setup complete. Run 'ib create <name>' to create a VM.\n\n")
	},
}

// setupLocalBin copies the ib binary to ~/.local/bin/ib and adds it to PATH
// in the user's shell RC file so future updates don't require sudo.
func setupLocalBin() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	localBin := filepath.Join(home, ".local", "bin")
	localIb := filepath.Join(localBin, "ib")

	if err := os.MkdirAll(localBin, 0755); err != nil {
		return
	}

	// Copy current binary to ~/.local/bin/ib
	exe, err := os.Executable()
	if err != nil {
		return
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return
	}
	if err := os.WriteFile(localIb, data, 0755); err != nil {
		return
	}

	// Add ~/.local/bin to PATH in shell RC files
	pathLine := `export PATH="$HOME/.local/bin:$PATH"`
	marker := ".local/bin"
	added := []string{}
	for _, rc := range shellRCFiles() {
		if prependPathInRC(rc, pathLine, marker) {
			added = append(added, rc)
		}
	}

	if len(added) > 0 {
		fmt.Printf("\n✓ Added ~/.local/bin to PATH in: %s\n", strings.Join(added, ", "))
		fmt.Printf("  Restart your shell or run: export PATH=\"$HOME/.local/bin:$PATH\"\n")
	}
}

// shellRCFiles returns the shell RC files to update based on the user's shell.
func shellRCFiles() []string {
	home, _ := os.UserHomeDir()
	shell := filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "zsh":
		return []string{filepath.Join(home, ".zshrc")}
	case "bash":
		// macOS uses .bash_profile; Linux uses .bashrc
		if _, err := os.Stat(filepath.Join(home, ".bash_profile")); err == nil {
			return []string{filepath.Join(home, ".bash_profile")}
		}
		return []string{filepath.Join(home, ".bashrc")}
	default:
		return []string{filepath.Join(home, ".profile")}
	}
}

// prependPathInRC adds pathLine to rc if marker is not already present.
// Returns true if the file was modified.
func prependPathInRC(rc, pathLine, marker string) bool {
	data, _ := os.ReadFile(rc)
	if strings.Contains(string(data), marker) {
		return false
	}
	entry := "\n# Added by ib\n" + pathLine + "\n"
	f, err := os.OpenFile(rc, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return false
	}
	defer f.Close()
	f.WriteString(entry)
	return true
}
