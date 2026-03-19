package config

import (
	"os"
)

type Config struct {
	APIKey             string
	DBPath             string
	VMNamespace        string
	SSHPiperNamespace  string
	StorageClass       string
	BaseImage          string
	IngressIP          string
	IngressDomain      string
	AuthURL            string
	SSHPiperIP         string
	IngressClass       string
	UpstreamSecretName string
}

func Load() *Config {
	return &Config{
		APIKey:             getEnv("INFRABOX_API_KEY", "changeme"),
		DBPath:             getEnv("INFRABOX_DB_PATH", "/data/infrabox.db"),
		VMNamespace:        getEnv("INFRABOX_VM_NAMESPACE", "infrabox-vms"),
		SSHPiperNamespace:  getEnv("INFRABOX_SSHPIPER_NS", "infrabox"),
		StorageClass:       getEnv("INFRABOX_STORAGE_CLASS", "local-path"),
		BaseImage:          getEnv("INFRABOX_BASE_IMAGE", "docker.io/library/infrabox-base:ubuntu-24.04"),
		IngressIP:          getEnv("INFRABOX_INGRESS_IP", ""),
		IngressDomain:      getEnv("INFRABOX_INGRESS_DOMAIN", ""),
		AuthURL:            getEnv("INFRABOX_AUTH_URL", ""),
		SSHPiperIP:         getEnv("INFRABOX_SSHPIPER_IP", ""),
		IngressClass:       getEnv("INFRABOX_INGRESS_CLASS", "nginx"),
		UpstreamSecretName: getEnv("INFRABOX_UPSTREAM_SECRET", "sshpiper-upstream-key"),
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
