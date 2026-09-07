package nodeagent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Name          string
	ListenHost    string
	ServicePort   int
	DataDir       string
	CertFile      string
	KeyFile       string
	XrayPath      string
	XrayAssetsDir string
	InstallMode   string
	UpdateChannel string
	Version       string
}

func LoadConfig() Config {
	dataDir := envString("ANTIMAGE_NODE_DATA_DIR", envString("ANTIMAGE_DATA_DIR", "/var/lib/antimage-node"))
	return Config{
		Name:          envString("ANTIMAGE_NODE_NAME", envString("ANTIMAGE_NODE_APP_NAME", "antimage-node")),
		ListenHost:    envString("SERVICE_HOST", "0.0.0.0"),
		ServicePort:   envInt("SERVICE_PORT", 62050),
		DataDir:       dataDir,
		CertFile:      envString("SSL_CERT_FILE", filepath.Join(dataDir, "cert.pem")),
		KeyFile:       envString("SSL_KEY_FILE", filepath.Join(dataDir, "cert.key")),
		XrayPath:      envString("XRAY_EXECUTABLE_PATH", filepath.Join(dataDir, "xray-core", executableName("xray"))),
		XrayAssetsDir: envString("XRAY_ASSETS_PATH", filepath.Join(dataDir, "xray-core")),
		InstallMode:   envString("ANTIMAGE_NODE_INSTALL_MODE", "binary"),
		UpdateChannel: envString("ANTIMAGE_NODE_UPDATE_CHANNEL", "stable"),
		Version:       envString("ANTIMAGE_NODE_VERSION", "dev"),
	}
}

func envString(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > 65535 {
		return fallback
	}
	return value
}

func executableName(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}
