package nodeagent

import (
	"encoding/json"
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
	XrayAPIPort   int
	InstallMode   string
	UpdateChannel string
	Version       string
}

func LoadConfig() Config {
	dataDir := envString("ANTIMAGE_NODE_DATA_DIR", envString("ANTIMAGE_DATA_DIR", "/var/lib/antimage-node"))
	appDir := envString("ANTIMAGE_NODE_APP_DIR", "/opt/antimage-node")
	installMode := envString("ANTIMAGE_NODE_INSTALL_MODE", "binary")
	metadataFile := envString(
		"ANTIMAGE_NODE_BINARY_METADATA_FILE",
		filepath.Join(appDir, ".binary-release.json"),
	)
	return Config{
		Name:          envString("ANTIMAGE_NODE_NAME", envString("ANTIMAGE_NODE_APP_NAME", "antimage-node")),
		ListenHost:    envString("SERVICE_HOST", "0.0.0.0"),
		ServicePort:   envInt("SERVICE_PORT", 62050),
		DataDir:       dataDir,
		CertFile:      envString("SSL_CERT_FILE", filepath.Join(dataDir, "cert.pem")),
		KeyFile:       envString("SSL_KEY_FILE", filepath.Join(dataDir, "cert.key")),
		XrayPath:      envString("XRAY_EXECUTABLE_PATH", filepath.Join(dataDir, "xray-core", executableName("xray"))),
		XrayAssetsDir: envString("XRAY_ASSETS_PATH", filepath.Join(dataDir, "xray-core")),
		XrayAPIPort:   envInt("XRAY_API_PORT", 10085),
		InstallMode:   installMode,
		UpdateChannel: envString("ANTIMAGE_NODE_UPDATE_CHANNEL", "stable"),
		Version:       configuredNodeVersion(installMode, metadataFile, envString("ANTIMAGE_NODE_VERSION", "dev")),
	}
}

func configuredNodeVersion(installMode, metadataFile, fallback string) string {
	if strings.EqualFold(strings.TrimSpace(installMode), "binary") {
		if version := binaryReleaseTag(metadataFile); version != "" {
			return version
		}
	}
	return envStringValue(fallback, "dev")
}

func binaryReleaseTag(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var metadata struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return ""
	}
	return strings.TrimSpace(metadata.Tag)
}

func envStringValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
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
