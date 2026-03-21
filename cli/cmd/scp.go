package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var scpCmd = &cobra.Command{
	Use:   "scp <src> <dst>",
	Short: "VMとの間でファイルを転送する (SCP)",
	Long: `VMとの間でファイルを転送する。
パスに「VM名:パス」形式を使うことで、ローカルとリモートを指定できる。

例:
  ib scp ./local.txt myvm:/tmp/          # ローカル → VM
  ib scp myvm:/tmp/remote.txt ./         # VM → ローカル
  ib scp -r ./dir myvm:/home/ubuntu/     # ディレクトリを再帰コピー`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()

		if cfg.SSHPiperIP == "" {
			fmt.Fprintln(os.Stderr, "ERROR: sshpiper_ip is not configured. Check ~/.ib/config.yaml")
			os.Exit(1)
		}

		scpBin, err := exec.LookPath("scp")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: scp command not found")
			os.Exit(1)
		}

		recursive, _ := cmd.Flags().GetBool("recursive")

		src := rewriteScpPath(args[0])
		dst := rewriteScpPath(args[1])

		scpArgs := []string{
			"scp",
			"-P", "2222",
			"-i", infraboxKeyPath(),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
		}
		if recursive {
			scpArgs = append(scpArgs, "-r")
		}
		scpArgs = append(scpArgs, src, dst)

		if err := syscall.Exec(scpBin, scpArgs, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	},
}

// rewriteScpPath は "vmname:/path" を "vmname@sshpiper_ip:/path" に変換する。
// コロンを含まないパスはローカルパスとしてそのまま返す。
func rewriteScpPath(path string) string {
	// すでに @ を含んでいればそのまま返す
	if strings.Contains(path, "@") {
		return path
	}
	idx := strings.Index(path, ":")
	if idx <= 0 {
		return path
	}
	vmName := path[:idx]
	remotePath := path[idx+1:]
	return vmName + "@" + cfg.SSHPiperIP + ":" + remotePath
}

func init() {
	scpCmd.Flags().BoolP("recursive", "r", false, "ディレクトリを再帰的にコピーする")
}
