package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "VMにSSH接続する",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		if cfg.SSHPiperIP == "" {
			fmt.Fprintln(os.Stderr, "ERROR: sshpiper_ip is not configured. Check ~/.ib/config.yaml")
			os.Exit(1)
		}

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: ssh command not found")
			os.Exit(1)
		}

		sshArgs := []string{
			"ssh",
			"-p", "2222",
			"-i", infraboxKeyPath(),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			name + "@" + cfg.SSHPiperIP,
		}
		if err := syscall.Exec(sshBin, sshArgs, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	},
}
