package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var pubKeyFile string

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		pubKey, err := loadPubKey(pubKeyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: cannot read SSH public key: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Creating VM '%s'...\n", name)
		start := time.Now()

		data, status, err := apiRequest("POST", "/v1/vms", map[string]string{
			"name":    name,
			"pub_key": pubKey,
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
		fmt.Printf("\n✓ Ready (%ds)\n\n", elapsed)
		fmt.Printf("  SSH:       ib ssh %s\n", vm.Name)
		fmt.Printf("  HTTPS URL: %s\n\n", vm.IngressURL)
	},
}

func init() {
	newCmd.Flags().StringVarP(&pubKeyFile, "key", "k", "", "SSH public key file (default: ~/.ssh/id_ed25519.pub)")
}

func loadPubKey(path string) (string, error) {
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home + "/.ssh/id_ed25519.pub"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
