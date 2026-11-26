package nodeagent

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestVerifiedWireGuardBindingsRequireExactAppliedPeers(t *testing.T) {
	keyA := base64.StdEncoding.EncodeToString(bytesForMonitoringKey(1))
	keyB := base64.StdEncoding.EncodeToString(bytesForMonitoringKey(2))
	prepared := preparedWireGuardRuntime{Tag: "games", InterfaceName: "wg-test", Inbound: wireGuardRuntimeInbound{Peers: []wireGuardRuntimePeer{
		{UserID: 42, PublicKey: keyA, Address: "10.69.1.1"},
		{UserID: 42, PublicKey: keyB, Address: "10.98.1.16"},
	}}}
	now := time.Now()
	valid := keyA + "\t10.69.1.1/32\n" + keyB + "\t10.98.1.16/32\n"
	bindings, err := verifiedWireGuardBindings(7, prepared, "boot:12", 9, now, valid)
	if err != nil || len(bindings) != 2 || bindings[0].UserID != bindings[1].UserID || bindings[0].Session == bindings[1].Session {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	replaced, err := verifiedWireGuardBindings(8, prepared, "boot:13", 10, now, valid)
	if err != nil || replaced[0].Session == bindings[0].Session || replaced[0].NodeID != 8 || replaced[0].Generation != 10 {
		t.Fatalf("interface replacement lost identity: %+v %v", replaced, err)
	}
	for _, raw := range []string{"", strings.Replace(valid, "/32", "/24", 1), strings.Replace(valid, "10.69.1.1", "10.69.1.2", 1), valid + keyA + "\t10.69.1.1/32\n", valid + "unknown\t10.0.0.1/32\n"} {
		if result, err := verifiedWireGuardBindings(7, prepared, "boot:12", 9, now, raw); err == nil || result != nil {
			t.Fatalf("accepted invalid snapshot %q", raw)
		}
	}
	if _, err := verifiedWireGuardBindings(7, prepared, "", 9, now, valid); err == nil {
		t.Fatal("accepted missing interface identity")
	}
	prepared.Inbound.Peers[1].Address = prepared.Inbound.Peers[0].Address
	if _, err := verifiedWireGuardBindings(7, prepared, "boot:12", 9, now, valid); err == nil {
		t.Fatal("accepted overlapping peer addresses")
	}
}

func bytesForMonitoringKey(value byte) []byte {
	result := make([]byte, 32)
	result[0] = value
	return result
}
