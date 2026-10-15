package nodeagent

import (
	"strings"
	"testing"
)

func TestParseWireGuardPeerSnapshotCapturesConfigurableState(t *testing.T) {
	publicKey := strings.Repeat("A", 43) + "="
	psk := strings.Repeat("B", 43) + "="
	raw := "private\tpublic\t51820\toff\n" +
		publicKey + "\t" + psk + "\t198.51.100.8:51820\t10.69.0.2/32,10.69.1.0/24\t1700000000\t12\t34\t25\n"
	peers, err := parseWireGuardPeerSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := peers[publicKey]
	if !ok {
		t.Fatalf("peer %q missing from snapshot", publicKey)
	}
	if peer.PresharedKey != psk || peer.Endpoint != "198.51.100.8:51820" || peer.PersistentKeepalive != 25 {
		t.Fatalf("peer configurable state = %+v", peer)
	}
	if strings.Join(peer.AllowedIPs, ",") != "10.69.0.2/32,10.69.1.0/24" {
		t.Fatalf("allowed IPs = %v", peer.AllowedIPs)
	}
}

func TestParseWireGuardPeerSnapshotRejectsIncompleteOrAmbiguousDump(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	peer := key + "\t(none)\t(none)\t10.69.0.2/32\t0\t0\t0\t0\n"
	cases := []struct {
		name string
		raw  string
	}{
		{name: "missing interface row", raw: peer},
		{name: "truncated peer row", raw: "private\tpublic\t51820\toff\n" + key + "\t(none)\t(none)\t10.69.0.2/32\t0\n"},
		{name: "duplicate peer", raw: "private\tpublic\t51820\toff\n" + peer + peer},
		{name: "malformed counter", raw: "private\tpublic\t51820\toff\n" + key + "\t(none)\t(none)\t10.69.0.2/32\tbad\t0\t0\t0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseWireGuardPeerSnapshot(tc.raw); err == nil {
				t.Fatal("expected malformed snapshot to be rejected")
			}
		})
	}
}

func TestWireGuardPeerInventoryDetectsPartialOrUnexpectedSnapshot(t *testing.T) {
	first := strings.Repeat("A", 43) + "="
	second := strings.Repeat("B", 43) + "="
	snapshot, err := parseWireGuardPeerSnapshot("private\tpublic\t51820\toff\n" + first + "\t(none)\t(none)\t10.69.0.2/32\t0\t0\t0\t0\n")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := parseWireGuardPeerList(first + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !wireGuardPeerInventoriesMatch(snapshot, listed) {
		t.Fatal("matching complete peer inventories were rejected")
	}
	listed, err = parseWireGuardPeerList(first + "\n" + second + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if wireGuardPeerInventoriesMatch(snapshot, listed) {
		t.Fatal("incomplete dump was accepted against the complete peer inventory")
	}
	if _, err := parseWireGuardPeerList(first + "\n" + first + "\n"); err == nil {
		t.Fatal("duplicate peer inventory key was accepted")
	}
}
