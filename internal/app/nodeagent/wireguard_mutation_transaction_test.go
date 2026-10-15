package nodeagent

import (
	"strings"
	"testing"
)

func TestWireGuardMutationPlanBuildsReverseTargetOnlyRollback(t *testing.T) {
	keyA, keyB, other := strings.Repeat("A", 43)+"=", strings.Repeat("B", 43)+"=", strings.Repeat("C", 43)+"="
	pskA := strings.Repeat("D", 43) + "="
	delta := wireGuardPeerDelta{
		Remove: []wireGuardRuntimePeer{{UserID: 7, PublicKey: keyA}},
		Set:    []wireGuardRuntimePeer{{UserID: 7, PublicKey: keyB, Address: "10.69.0.2"}},
	}
	snapshot := map[string]wireGuardKernelPeer{
		keyA:  {PublicKey: keyA, PresharedKey: pskA, Endpoint: "198.51.100.4:51820", AllowedIPs: []string{"10.69.0.2/32"}, PersistentKeepalive: 25, ReceivedBytes: 90, SentBytes: 120},
		other: {PublicKey: other, AllowedIPs: []string{"10.69.0.3/32"}},
	}
	plan, err := planWireGuardKernelMutation(delta, snapshot, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Forward) != 2 || plan.Forward[0].Operation != "remove" || plan.Forward[0].Peer.PublicKey != keyA || plan.Forward[1].Operation != "set" || plan.Forward[1].Peer.PublicKey != keyB {
		t.Fatalf("forward mutation plan = %+v", plan.Forward)
	}
	if len(plan.Rollback) != 2 || plan.Rollback[0].Operation != "remove" || plan.Rollback[0].Peer.PublicKey != keyB || plan.Rollback[1].Operation != "set" || plan.Rollback[1].Peer.PublicKey != keyA {
		t.Fatalf("reverse rollback plan = %+v", plan.Rollback)
	}
	old := plan.Rollback[1].Peer
	if old.PresharedKey != pskA || old.Endpoint != "198.51.100.4:51820" || old.PersistentKeepalive != 25 || len(old.AllowedIPs) != 1 || old.AllowedIPs[0] != "10.69.0.2/32" {
		t.Fatalf("rollback lost configurable pre-mutation state: %+v", old)
	}
	if old.ReceivedBytes != 90 || old.SentBytes != 120 {
		t.Fatalf("snapshot counters were not retained for accounting: %+v", old)
	}
	if steps := plan.RollbackForAttempted(1); len(steps) != 1 || steps[0].Operation != "set" || steps[0].Peer.PublicKey != keyA {
		t.Fatalf("rollback after first attempted step = %+v", steps)
	}
	if steps := plan.RollbackForAttempted(2); len(steps) != 2 || steps[0].Peer.PublicKey != keyB || steps[1].Peer.PublicKey != keyA {
		t.Fatalf("rollback after second attempted step = %+v", steps)
	}
	for _, step := range append(plan.Forward, plan.Rollback...) {
		if step.Peer.PublicKey == other {
			t.Fatal("mutation plan touched an unrelated peer")
		}
	}
}

func TestWireGuardMutationPlanRestoresExistingPeerWithoutTouchingEndpointOnApply(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	oldPSK, newPSK := strings.Repeat("B", 43)+"=", strings.Repeat("C", 43)+"="
	snapshot := map[string]wireGuardKernelPeer{
		key: {PublicKey: key, PresharedKey: oldPSK, Endpoint: "198.51.100.8:40000", AllowedIPs: []string{"10.69.0.2/32"}, PersistentKeepalive: 15},
	}
	delta := wireGuardPeerDelta{Set: []wireGuardRuntimePeer{{UserID: 3, PublicKey: key, PresharedKey: newPSK, Address: "10.69.0.9"}}}
	plan, err := planWireGuardKernelMutation(delta, snapshot, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Forward[0].Peer; got.Endpoint != "198.51.100.8:40000" || got.PersistentKeepalive != 15 {
		t.Fatalf("live endpoint/keepalive should be retained in expected state: %+v", got)
	}
	if got := plan.Rollback[0].Peer; got.PresharedKey != oldPSK || got.Endpoint != "198.51.100.8:40000" || got.PersistentKeepalive != 15 || got.AllowedIPs[0] != "10.69.0.2/32" {
		t.Fatalf("rollback state = %+v", got)
	}
}

func TestWireGuardMutationPlanRejectsNonTargetPeer(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	_, err := planWireGuardKernelMutation(wireGuardPeerDelta{Remove: []wireGuardRuntimePeer{{UserID: 8, PublicKey: key}}}, map[string]wireGuardKernelPeer{key: {PublicKey: key}}, 7, "")
	if err == nil {
		t.Fatal("non-target user peer was accepted into mutation plan")
	}
}
