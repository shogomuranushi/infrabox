package config

import (
	"os"
	"strconv"
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
	MaxVMsPerUser      int
	UserCPUQuota       string // ResourceQuota requests.cpu per user namespace (e.g. "2")
	UserMemoryQuota    string // ResourceQuota requests.memory per user namespace (e.g. "8Gi")
}

func Load() *Config {
	return &Config{
		APIKey:             getEnv("INFRABOX_API_KEY", ""),
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
		MaxVMsPerUser:      getEnvInt("INFRABOX_MAX_VMS_PER_USER", 10),
		UserCPUQuota:       getEnv("INFRABOX_USER_CPU_QUOTA", "2"),
		UserMemoryQuota:    getEnv("INFRABOX_USER_MEMORY_QUOTA", "8Gi"),
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}
