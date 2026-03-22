package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	APIKey          string
	DBPath          string
	VMNamespace     string
	StorageClass    string
	BaseImage       string
	IngressIP       string
	IngressDomain   string
	AuthURL         string
	IngressClass    string
	MaxVMsPerUser   int
	UserCPUQuota    string // ResourceQuota requests.cpu per user namespace (e.g. "2")
	UserMemoryQuota string // ResourceQuota requests.memory per user namespace (e.g. "8Gi")
	VMNodeSelector          map[string]string // nodeSelector for VM pods (e.g. infrabox-role=vm-worker)
	RcloneDriveClientID     string            // optional: OAuth client ID for rclone Google Drive sync
	RcloneDriveClientSecret string            // optional: OAuth client secret for rclone Google Drive sync
}

func Load() *Config {
	return &Config{
		APIKey:          getEnv("INFRABOX_API_KEY", ""),
		DBPath:          getEnv("INFRABOX_DB_PATH", "/data/infrabox.db"),
		VMNamespace:     getEnv("INFRABOX_VM_NAMESPACE", "infrabox-vms"),
		StorageClass:    getEnv("INFRABOX_STORAGE_CLASS", "local-path"),
		BaseImage:       getEnv("INFRABOX_BASE_IMAGE", "docker.io/library/infrabox-base:ubuntu-24.04"),
		IngressIP:       getEnv("INFRABOX_INGRESS_IP", ""),
		IngressDomain:   getEnv("INFRABOX_INGRESS_DOMAIN", ""),
		AuthURL:         getEnv("INFRABOX_AUTH_URL", ""),
		IngressClass:    getEnv("INFRABOX_INGRESS_CLASS", "nginx"),
		MaxVMsPerUser:   getEnvInt("INFRABOX_MAX_VMS_PER_USER", 10),
		UserCPUQuota:    getEnv("INFRABOX_USER_CPU_QUOTA", "2"),
		UserMemoryQuota: getEnv("INFRABOX_USER_MEMORY_QUOTA", "8Gi"),
		VMNodeSelector:          parseNodeSelector(getEnv("INFRABOX_VM_NODE_SELECTOR", "")),
		RcloneDriveClientID:     getEnv("INFRABOX_RCLONE_DRIVE_CLIENT_ID", ""),
		RcloneDriveClientSecret: getEnv("INFRABOX_RCLONE_DRIVE_CLIENT_SECRET", ""),
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// parseNodeSelector parses "key1=val1,key2=val2" into a map.
func parseNodeSelector(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}
