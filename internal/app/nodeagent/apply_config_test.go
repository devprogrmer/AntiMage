package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/devprogrmer/AntiMage/internal/proto/node/v1"
)

func TestApplyConfigValidationFailurePreservesLastGoodConfigAndRevision(t *testing.T) {
	withRuntimeCommand(t, "validate-fail")

	dataDir := t.TempDir()
	xrayPath := filepath.Join(dataDir, "xray")
	if err := os.WriteFile(xrayPath, []byte("placeholder"), 0755); err != nil {
		t.Fatalf("create fake xray: %v", err)
	}

	configPath := filepath.Join(dataDir, "xray-config.json")
	const lastGood = `{"log":{"loglevel":"warning"}}`
	if err := os.WriteFile(configPath, []byte(lastGood), 0644); err != nil {
		t.Fatalf("write last-good config: %v", err)
	}

	s := New(Config{
		DataDir:       dataDir,
		XrayPath:      xrayPath,
		XrayAssetsDir: dataDir,
	})
	s.lastConfig = configPath
	s.appliedRev = 7

	_, err := s.applyConfig(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"broken":true}`,
		DesiredRevision: 99,
	}, "config synced")
	if err == nil {
		t.Fatal("expected invalid config to fail")
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read last-good config: %v", err)
	}
	if string(got) != lastGood {
		t.Fatalf("last-good config was overwritten: %s", got)
	}

	if s.appliedRev != 7 {
		t.Fatalf("applied revision advanced after rejected config: got %d want 7", s.appliedRev)
	}
}
