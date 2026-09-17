package nodeagent

import "testing"

func TestParseL2TPPPPInterfaces(t *testing.T) {
	raw := `12: eth0 inet 192.0.2.10/24 scope global eth0
20: ppp0 inet 10.67.0.1 peer 10.67.242.180/32 scope global ppp0
21: ppp1 inet 10.67.0.1 peer 10.67.78.60/32 scope global ppp1`

	got := parseL2TPPPPInterfaces(raw)
	if len(got) != 2 {
		t.Fatalf(
			"expected 2 PPP sessions, got %d",
			len(got),
		)
	}

	if got[0].Interface != "ppp0" ||
		got[0].PeerIP != "10.67.242.180" {
		t.Fatalf("unexpected first session: %#v", got[0])
	}

	if got[1].Interface != "ppp1" ||
		got[1].PeerIP != "10.67.78.60" {
		t.Fatalf("unexpected second session: %#v", got[1])
	}
}

func TestParseL2TPPPPInterfacesRejectsUnknownInterfaces(
	t *testing.T,
) {
	raw := `20: tun0 inet 10.0.0.1 peer 10.0.0.2/32 scope global tun0
21: pppx inet 10.0.0.1 peer 10.0.0.3/32 scope global pppx`

	got := parseL2TPPPPInterfaces(raw)
	if len(got) != 0 {
		t.Fatalf("expected no PPP sessions, got %#v", got)
	}
}

func TestL2TPUsageDelta(t *testing.T) {
	if got := l2TPUsageDelta(150, 100, true); got != 50 {
		t.Fatalf("expected delta 50, got %d", got)
	}

	if got := l2TPUsageDelta(30, 100, true); got != 30 {
		t.Fatalf(
			"counter reset should return current total, got %d",
			got,
		)
	}

	if got := l2TPUsageDelta(30, 0, false); got != 30 {
		t.Fatalf(
			"first sample should return current total, got %d",
			got,
		)
	}
}

func TestL2TPUsageBatchProtoOnlineZeroTraffic(
	t *testing.T,
) {
	batch := l2TPUsageBatchProto(
		&l2TPUsagePendingBatch{
			BatchID: "l2tp-test",
			Samples: []l2TPUsageSample{
				{
					UserID:     42,
					InboundTag: "l2tp-main",
					Online:     true,
					IP:         "10.67.242.180",
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

	if got := batch.GetStats()[0].GetUid(); got != "online:l2tp:42" {
		t.Fatalf("unexpected online UID: %q", got)
	}

	if len(batch.GetOnlineIps()) != 1 {
		t.Fatal("expected 1 online IP record")
	}

	if got := batch.GetOnlineIps()[0].GetUid(); got != "l2tp:42" {
		t.Fatalf("unexpected online IP UID: %q", got)
	}

	ips := batch.GetOnlineIps()[0].GetIps()
	if len(ips) != 1 ||
		ips[0].GetIp() != "10.67.242.180" {
		t.Fatalf("unexpected online IP payload: %#v", ips)
	}
}
