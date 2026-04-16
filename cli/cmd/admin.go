package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)


var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Admin operations (requires admin API key)",
}

var adminInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Save admin API key",
	Run: func(cmd *cobra.Command, args []string) {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Admin API key: ")
		key, _ := reader.ReadString('\n')
		key = strings.TrimSpace(key)
		if key == "" {
			fmt.Fprintln(os.Stderr, "ERROR: admin key is required")
			os.Exit(1)
		}
		cfg.AdminKey = key
		if err := saveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to save config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Admin key saved.")
	},
}

var adminInviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Manage invitation codes",
}

var adminInviteCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Generate a new invitation code",
	Run: func(cmd *cobra.Command, args []string) {
		mustAdminConfig()
		data, status, err := doRequest("POST", "/v1/invitations", nil, cfg.AdminKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}
		var resp struct {
			Code string `json:"code"`
		}
		json.Unmarshal(data, &resp)
		fmt.Printf("code: %s\n", resp.Code)
	},
}

var adminInviteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all invitation codes",
	Run: func(cmd *cobra.Command, args []string) {
		mustAdminConfig()
		data, status, err := doRequest("GET", "/v1/invitations", nil, cfg.AdminKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		if status != 200 {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", extractError(data, status))
			os.Exit(1)
		}
		var resp struct {
			InvitationCodes []struct {
				ID        string  `json:"id"`
				Used      bool    `json:"used"`
				UsedBy    string  `json:"used_by"`
				CreatedAt string  `json:"created_at"`
				UsedAt    *string `json:"used_at"`
			} `json:"invitation_codes"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to parse response\n")
			os.Exit(1)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tUSED\tUSED BY\tCREATED")
		fmt.Fprintln(w, "----\t----\t-------\t-------")
		for _, ic := range resp.InvitationCodes {
			usedBy := "-"
			if ic.UsedBy != "" {
				usedBy = ic.UsedBy
			}
			used := "no"
			if ic.Used {
				used = "yes"
			}
			created := formatTime(ic.CreatedAt)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ic.ID[:8]+"...", used, usedBy, created)
		}
		w.Flush()
	},
}

func mustAdminConfig() {
	mustConfig()
	if cfg.AdminKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: admin key is not set. Run 'ib admin init'.")
		os.Exit(1)
	}
}

func formatTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func init() {
	adminInviteCmd.AddCommand(adminInviteCreateCmd, adminInviteListCmd)
	adminCmd.AddCommand(adminInitCmd, adminInviteCmd)
}
