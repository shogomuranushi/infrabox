package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Embedded at build time via -ldflags.
var defaultEndpoint string
var Version = "dev"

type Config struct {
	Endpoint        string `yaml:"endpoint"`
	APIKey          string `yaml:"api_key"`
	AdminKey        string `yaml:"admin_key,omitempty"`
	AutoUploadPaste bool   `yaml:"auto_upload_paste,omitempty"`
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
	rootCmd.AddCommand(createCmd, listCmd, deleteCmd, sshCmd, scpCmd, restartCmd, renameCmd, authCmd, initCmd, upgradeCmd, versionCmd, adminCmd, topCmd, setupScriptCmd, syncCmd, forwardCmd)
}

func loadConfig() {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		// no config file — initialize with defaults
		cfg = Config{
			Endpoint: getEnv("INFRABOX_ENDPOINT", defaultEndpoint),
			APIKey:   "",
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
	// environment variable overrides
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
