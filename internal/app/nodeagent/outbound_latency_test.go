package nodeagent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOutboundLatencyTarget(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{
			name:     "http",
			rawURL:   "http://example.com/test",
			wantHost: "example.com",
			wantPort: 80,
		},
		{
			name:     "https",
			rawURL:   "https://example.com/test",
			wantHost: "example.com",
			wantPort: 443,
		},
		{
			name:     "custom port",
			rawURL:   "https://example.com:8443/test",
			wantHost: "example.com",
			wantPort: 8443,
		},
		{
			name:    "invalid scheme",
			rawURL:  "ftp://example.com/file",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := outboundLatencyTarget(tc.rawURL)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf(
					"target=%s:%d want=%s:%d",
					host,
					port,
					tc.wantHost,
					tc.wantPort,
				)
			}
		})
	}
}

func TestBuildOutboundLatencyConfigRoutesSelectedOutbound(t *testing.T) {
	outbounds := []map[string]any{
		{
			"tag":      "direct",
			"protocol": "freedom",
		},
		{
			"tag":      "proxy-one",
			"protocol": "socks",
			"settings": map[string]any{
				"servers": []any{
					map[string]any{
						"address": "127.0.0.1",
						"port":    1080,
					},
				},
			},
		},
	}

	raw, err := buildOutboundLatencyConfig(
		"proxy-one",
		32123,
		outbounds,
	)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	inbounds, ok := config["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("unexpected inbounds: %#v", config["inbounds"])
	}

	inbound, ok := inbounds[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected inbound: %#v", inbounds[0])
	}

	if inbound["tag"] != "antimage-outbound-probe" {
		t.Fatalf("unexpected probe inbound tag: %#v", inbound["tag"])
	}
	if inbound["listen"] != "127.0.0.1" {
		t.Fatalf("probe must listen on loopback: %#v", inbound["listen"])
	}
	if intValue(inbound["port"]) != 32123 {
		t.Fatalf("unexpected probe port: %#v", inbound["port"])
	}

	routing, ok := config["routing"].(map[string]any)
	if !ok {
		t.Fatalf("missing routing config")
	}

	rules, ok := routing["rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("unexpected routing rules: %#v", routing["rules"])
	}

	rule, ok := rules[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected routing rule: %#v", rules[0])
	}

	if rule["outboundTag"] != "proxy-one" {
		t.Fatalf(
			"probe traffic routed to %#v instead of selected outbound",
			rule["outboundTag"],
		)
	}

	configOutbounds, ok := config["outbounds"].([]any)
	if !ok || len(configOutbounds) != 2 {
		t.Fatalf("outbounds were not preserved: %#v", config["outbounds"])
	}
}

func TestOutboundLatencyReportsUnavailableXray(t *testing.T) {
	dataDir := t.TempDir()

	s := New(Config{
		DataDir:  dataDir,
		XrayPath: filepath.Join(dataDir, "missing-xray"),
	})

	res, err := s.TestOutbound(t.Context(), &nodev1.OutboundTestRequest{
		OutboundTag: "direct",
		AllOutboundsJson: `[
{
"tag":"direct",
"protocol":"freedom",
"settings":{}
}
]`,
		TestUrl:  "https://example.com/generate_204",
		TestType: "latency",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}

	if res.GetSuccess() {
		t.Fatal("expected latency test to fail without Xray")
	}

	if !strings.Contains(
		res.GetError(),
		"Xray executable is unavailable",
	) {
		t.Fatalf("unexpected error: %q", res.GetError())
	}

	if res.GetAddress() != "example.com" {
		t.Fatalf("address=%q", res.GetAddress())
	}

	if res.GetPort() != 443 {
		t.Fatalf("port=%d want=443", res.GetPort())
	}

	if res.GetTestType() != "latency" {
		t.Fatalf("test type=%q", res.GetTestType())
	}
}
