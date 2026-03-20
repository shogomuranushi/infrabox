package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ビルド時に -ldflags で埋め込む
var defaultEndpoint string
var defaultSSHPiperIP string
var Version = "dev"

type Config struct {
	Endpoint   string `yaml:"endpoint"`
	APIKey     string `yaml:"api_key"`
	SSHPiperIP string `yaml:"sshpiper_ip"`
}

var cfg Config

var rootCmd = &cobra.Command{
	Use:   "ib",
	Short: "InfraBox CLI",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(loadConfig, checkUpdateBackground)
	rootCmd.AddCommand(newCmd, listCmd, deleteCmd, sshCmd, restartCmd, renameCmd, initCmd, upgradeCmd, versionCmd)
}

func loadConfig() {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		// 設定ファイルがなければデフォルト値で自動生成
		cfg = Config{
			Endpoint:   getEnv("INFRABOX_ENDPOINT", defaultEndpoint),
			SSHPiperIP: getEnv("INFRABOX_SSHPIPER_IP", defaultSSHPiperIP),
			APIKey:     "",
		}
		if cfg.Endpoint != "" {
			if err := saveConfig(cfg); err == nil {
				fmt.Fprintf(os.Stderr, "Config file created: %s\n", path)
				fmt.Fprintln(os.Stderr, "Run 'ib init' to set up your API key.")
			}
		}
		return
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config parse error: %v\n", err)
		os.Exit(1)
	}
	// 環境変数でオーバーライド
	if v := os.Getenv("INFRABOX_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("INFRABOX_API_KEY"); v != "" {
		cfg.APIKey = v
	}
}

func saveConfig(c Config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ib", "config.yaml")
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
