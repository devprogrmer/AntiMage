package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestParsePPTPPPPInterfaces(t *testing.T) {
	raw := `12: eth0 inet 192.0.2.10/24 scope global eth0
20: ppp0 inet 10.68.0.1 peer 10.68.0.42/32 scope global ppp0
21: ppp1 inet 10.68.0.1 peer 10.68.0.60/32 scope global ppp1`

	got := parsePPTPPPPInterfaces(raw)
	if len(got) != 2 {
		t.Fatalf(
			"expected 2 PPP sessions, got %d",
			len(got),
		)
	}

	if got[0].Interface != "ppp0" ||
		got[0].PeerIP != "10.68.0.42" {
		t.Fatalf("unexpected first session: %#v", got[0])
	}

	if got[1].Interface != "ppp1" ||
		got[1].PeerIP != "10.68.0.60" {
		t.Fatalf("unexpected second session: %#v", got[1])
	}
}

func TestParsePPTPPPPInterfacesRejectsUnknownInterfaces(
	t *testing.T,
) {
	raw := `20: tun0 inet 10.0.0.1 peer 10.0.0.2/32 scope global tun0
21: pppx inet 10.0.0.1 peer 10.0.0.3/32 scope global pppx`

	got := parsePPTPPPPInterfaces(raw)
	if len(got) != 0 {
		t.Fatalf("expected no PPP sessions, got %#v", got)
	}
}

func TestPPTPUsageDelta(t *testing.T) {
	if got := pptpUsageDelta(150, 100, true); got != 50 {
		t.Fatalf("expected delta 50, got %d", got)
	}

	if got := pptpUsageDelta(30, 100, true); got != 30 {
		t.Fatalf(
			"counter reset should return current total, got %d",
			got,
		)
	}

	if got := pptpUsageDelta(30, 0, false); got != 30 {
		t.Fatalf(
			"first sample should return current total, got %d",
			got,
		)
	}
}

func TestPPTPUsageBatchProtoOnlineZeroTraffic(
	t *testing.T,
) {
	batch := pptpUsageBatchProto(
		&pptpUsagePendingBatch{
			BatchID: "pptp-test",
			Samples: []pptpUsageSample{
				{
					UserID:     42,
					InboundTag: "pptp-main",
					Online:     true,
					IP:         "10.68.0.42",
				},
			},
		},
	)

	if len(batch.GetStats()) != 1 {
		t.Fatalf(
			"expected 1 stat, got %d",
			len(batch.GetStats()),
		)
	}

	if got := batch.GetStats()[0].GetUid(); got != "online:pptp:42" {
		t.Fatalf("unexpected online UID: %q", got)
	}

	if len(batch.GetOnlineIps()) != 1 {
		t.Fatal("expected 1 online IP record")
	}

	if got := batch.GetOnlineIps()[0].GetUid(); got != "pptp:42" {
		t.Fatalf("unexpected online IP UID: %q", got)
	}

	ips := batch.GetOnlineIps()[0].GetIps()
	if len(ips) != 1 ||
		ips[0].GetIp() != "10.68.0.42" {
		t.Fatalf("unexpected online IP payload: %#v", ips)
	}
}

func TestPreparePPTPInboundWritesUsageHelper(t *testing.T) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})

	configPath, err := server.preparePPTPInbound(
		pptpRuntimeInbound{
			Tag:        "pptp-main",
			Port:       1723,
			TunnelPort: 41942,
			Settings: map[string]any{
				"ipv4_pool_cidr": "10.68.0.0/24",
			},
			Users: []pptpRuntimeUser{
				{
					UserID:      42,
					Username:    "alice",
					VPNUsername: "alice",
					Password:    "secret",
					IPv4Address: "10.68.0.42",
					Status:      "active",
				},
				{
					UserID:      43,
					Username:    "disabled",
					VPNUsername: "disabled",
					Password:    "secret",
					IPv4Address: "10.68.0.43",
					Status:      "disabled",
				},
			},
		},
		nativeRuntimeSessionCallback{},
	)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(
		filepath.Join(filepath.Dir(configPath), "usage-helper.json"),
	)
	if err != nil {
		t.Fatal(err)
	}

	var cfg pptpUsageRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.InboundTag != "pptp-main" {
		t.Fatalf("inbound tag = %q", cfg.InboundTag)
	}

	if got := cfg.Users["10.68.0.42"]; got != 42 {
		t.Fatalf("user mapping = %d, want 42", got)
	}

	if _, exists := cfg.Users["10.68.0.43"]; exists {
		t.Fatal("disabled user must not be present in usage mapping")
	}
}

func TestAckPPTPUserUsageIsIdempotent(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	server.pptpUsageLoaded = true
	server.pptpUsageBaseline = map[string]uint64{}
	server.pptpUsagePending = &pptpUsagePendingBatch{
		BatchID: "pptp-test",
		NextBaseline: map[string]uint64{
			"pptp-main|ppp0|10.68.0.42": 1234,
		},
	}

	req := &nodev1.AckUsageRequest{BatchId: "pptp-test"}

	first, err := server.ackPPTPUserUsage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.GetAcknowledged() {
		t.Fatal("first ACK was not acknowledged")
	}

	if got := server.pptpUsageBaseline["pptp-main|ppp0|10.68.0.42"]; got != 1234 {
		t.Fatalf("baseline = %d, want 1234", got)
	}
	if server.pptpUsagePending != nil {
		t.Fatal("pending batch was not cleared")
	}

	second, err := server.ackPPTPUserUsage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !second.GetAcknowledged() {
		t.Fatal("repeated ACK must remain acknowledged")
	}
}
