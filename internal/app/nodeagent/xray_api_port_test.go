package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestXrayAPIPortFromRuntimeConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		want    int
		found   bool
		wantErr bool
	}{
		{
			name:   "numeric api port",
			config: `{"inbounds":[{"tag":"API_INBOUND","port":10090}]}`,
			want:   10090,
			found:  true,
		},
		{
			name:   "numeric string api port",
			config: `{"inbounds":[{"tag":"API_INBOUND","port":"10091"}]}`,
			want:   10091,
			found:  true,
		},
		{
			name:   "ignores unrelated inbound",
			config: `{"inbounds":[{"tag":"vless-in","port":443}]}`,
			found:  false,
		},
		{
			name:    "rejects zero",
			config:  `{"inbounds":[{"tag":"API_INBOUND","port":0}]}`,
			wantErr: true,
		},
		{
			name:    "rejects too large",
			config:  `{"inbounds":[{"tag":"API_INBOUND","port":70000}]}`,
			wantErr: true,
		},
		{
			name:    "rejects invalid string",
			config:  `{"inbounds":[{"tag":"API_INBOUND","port":"invalid"}]}`,
			wantErr: true,
		},
		{
			name:    "rejects malformed json",
			config:  `{"inbounds":[`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found, err := xrayAPIPortFromRuntimeConfig(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != tt.found {
				t.Fatalf("found=%v, want %v", found, tt.found)
			}
			if got != tt.want {
				t.Fatalf("port=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestSyncXrayAPIPortFromRuntimeConfigOverridesBootstrapFallback(t *testing.T) {
	server := New(Config{
		XrayAPIPort: 10085,
	})

	port, found, err := server.syncXrayAPIPortFromRuntimeConfig(
		`{"inbounds":[{"tag":"API_INBOUND","port":10090}]}`,
	)
	if err != nil {
		t.Fatalf("sync API port: %v", err)
	}
	if !found {
		t.Fatal("expected API_INBOUND to be found")
	}
	if port != 10090 {
		t.Fatalf("returned port=%d, want 10090", port)
	}

	server.mu.Lock()
	got := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if got != 10090 {
		t.Fatalf("effective Xray API port=%d, want 10090", got)
	}
}

func TestSyncXrayAPIPortFromRuntimeConfigPreservesFallbackWhenMissing(t *testing.T) {
	server := New(Config{
		XrayAPIPort: 10085,
	})

	_, found, err := server.syncXrayAPIPortFromRuntimeConfig(
		`{"inbounds":[{"tag":"vless-in","port":443}]}`,
	)
	if err != nil {
		t.Fatalf("sync API port: %v", err)
	}
	if found {
		t.Fatal("did not expect API_INBOUND")
	}

	server.mu.Lock()
	got := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if got != 10085 {
		t.Fatalf("bootstrap fallback changed to %d, want 10085", got)
	}
}

func TestSyncXrayAPIPortFromRuntimeConfigDoesNotApplyInvalidPort(t *testing.T) {
	server := New(Config{
		XrayAPIPort: 10085,
	})

	_, _, err := server.syncXrayAPIPortFromRuntimeConfig(
		`{"inbounds":[{"tag":"API_INBOUND","port":0}]}`,
	)
	if err == nil {
		t.Fatal("expected invalid API port error")
	}

	server.mu.Lock()
	got := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if got != 10085 {
		t.Fatalf("invalid runtime port changed effective port to %d", got)
	}
}
func TestSyncXrayAPIPortFromRuntimeConfigRestoresBootstrapFallback(t *testing.T) {
	server := New(Config{
		XrayAPIPort: 10085,
	})

	_, found, err := server.syncXrayAPIPortFromRuntimeConfig(
		`{"inbounds":[{"tag":"API_INBOUND","port":10090}]}`,
	)
	if err != nil {
		t.Fatalf("first sync API port: %v", err)
	}
	if !found {
		t.Fatal("expected first API_INBOUND")
	}

	server.mu.Lock()
	firstPort := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if firstPort != 10090 {
		t.Fatalf("first effective port=%d, want 10090", firstPort)
	}

	_, found, err = server.syncXrayAPIPortFromRuntimeConfig(
		`{"inbounds":[{"tag":"vless-in","port":443}]}`,
	)
	if err != nil {
		t.Fatalf("second sync API port: %v", err)
	}
	if found {
		t.Fatal("did not expect API_INBOUND in second config")
	}

	server.mu.Lock()
	got := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if got != 10085 {
		t.Fatalf(
			"effective API port=%d after API_INBOUND removal; want bootstrap fallback 10085",
			got,
		)
	}
}

func TestApplyConfigDoesNotChangeXrayAPIPortWhenPersistenceFails(t *testing.T) {
	tmp := t.TempDir()

	blockedDataDir := filepath.Join(tmp, "not-a-directory")
	if err := os.WriteFile(blockedDataDir, []byte("blocked"), 0600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	server := New(Config{
		DataDir:     blockedDataDir,
		XrayAPIPort: 10085,
	})

	_, err := server.applyConfig(
		context.Background(),
		&nodev1.RuntimeConfigRequest{
			ConfigJson: `{"inbounds":[{"tag":"API_INBOUND","port":10090}]}`,
		},
		"test",
	)
	if err == nil {
		t.Fatal("expected persistence failure")
	}

	server.mu.Lock()
	got := server.cfg.XrayAPIPort
	server.mu.Unlock()

	if got != 10085 {
		t.Fatalf(
			"failed config persistence changed effective API port to %d; want 10085",
			got,
		)
	}
}
