package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMasterCertificateTrustPrecedence(t *testing.T) {
	t.Setenv("ANTIMAGE_NODE_MASTER_CERT_FILE", "")
	t.Setenv("SSL_CLIENT_CERT_FILE", "")
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "node.pem"))
	if cfg := LoadConfig(); cfg.MasterCertFile != cfg.CertFile {
		t.Fatalf("existing install bundle trust = %q, node cert = %q", cfg.MasterCertFile, cfg.CertFile)
	}
	t.Setenv("SSL_CLIENT_CERT_FILE", "legacy-master.pem")
	if got := LoadConfig().MasterCertFile; got != "legacy-master.pem" {
		t.Fatalf("legacy explicit client trust = %q", got)
	}
	t.Setenv("ANTIMAGE_NODE_MASTER_CERT_FILE", "explicit-master.pem")
	if got := LoadConfig().MasterCertFile; got != "explicit-master.pem" {
		t.Fatalf("explicit master trust = %q", got)
	}
}

func TestConfiguredNodeVersion(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), ".binary-release.json")
	if err := os.WriteFile(metadataPath, []byte(`{"tag":"dev-ad4bf47"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		installMode string
		path        string
		fallback    string
		want        string
	}{
		{
			name:        "binary metadata is authoritative over a stale environment value",
			installMode: "binary",
			path:        metadataPath,
			fallback:    "dev-49d2da6",
			want:        "dev-ad4bf47",
		},
		{
			name:        "docker keeps its configured version",
			installMode: "docker",
			path:        metadataPath,
			fallback:    "container-version",
			want:        "container-version",
		},
		{
			name:        "binary falls back when metadata is missing",
			installMode: "binary",
			path:        filepath.Join(t.TempDir(), "missing.json"),
			fallback:    "dev",
			want:        "dev",
		},
		{
			name:        "empty fallback uses the safe default",
			installMode: "docker",
			fallback:    " ",
			want:        "dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := configuredNodeVersion(tt.installMode, tt.path, tt.fallback); got != tt.want {
				t.Fatalf("configuredNodeVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
