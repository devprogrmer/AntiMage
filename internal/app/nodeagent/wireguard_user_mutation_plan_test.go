package nodeagent

import "testing"

func TestPlanWireGuardUserAddChangesOnlyTargetUser(t *testing.T) {
	current := []wireGuardRuntimePeer{{UserID: 2, PublicKey: "other", Address: "10.69.0.3"}}
	desired := []wireGuardRuntimePeer{{UserID: 1, PublicKey: "new-user", Address: "10.69.0.2"}}
	delta, err := planWireGuardUserPeerDelta(current, 1, "add", desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Set) != 1 || delta.Set[0].PublicKey != "new-user" || len(delta.Remove) != 0 {
		t.Fatalf("delta = %+v, want one set for target user only", delta)
	}
	if len(delta.Peers) != 2 || delta.Peers[0].UserID != 1 || delta.Peers[1].PublicKey != "other" {
		t.Fatalf("merged peers = %+v, unrelated peer was not preserved", delta.Peers)
	}
	if len(delta.Current) != 1 || delta.Current[0].PublicKey != "other" {
		t.Fatalf("pre-mutation peer inventory = %+v, want complete baseline", delta.Current)
	}
}

func TestPlanWireGuardUserUpdateReconcilesOnlyTargetDevices(t *testing.T) {
	current := []wireGuardRuntimePeer{
		{UserID: 1, PublicKey: "device-a", Address: "10.69.0.2"},
		{UserID: 1, PublicKey: "stale-device", Address: "10.69.0.4"},
		{UserID: 2, PublicKey: "other", Address: "10.69.0.3"},
	}
	desired := []wireGuardRuntimePeer{
		{UserID: 1, PublicKey: "device-a", Address: "10.69.0.5"},
		{UserID: 1, PublicKey: "device-b", Address: "10.69.0.6"},
	}
	delta, err := planWireGuardUserPeerDelta(current, 1, "update", desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Remove) != 1 || delta.Remove[0].PublicKey != "stale-device" {
		t.Fatalf("removed peers = %+v, want only stale target device", delta.Remove)
	}
	if len(delta.Set) != 2 {
		t.Fatalf("set peers = %+v, want changed device-a and new device-b", delta.Set)
	}
	if len(delta.Peers) != 3 || delta.Peers[2].PublicKey != "other" {
		t.Fatalf("merged peers = %+v, unrelated peer was not preserved", delta.Peers)
	}
}

func TestPlanWireGuardUserRemoveAndIdenticalUpdate(t *testing.T) {
	current := []wireGuardRuntimePeer{
		{UserID: 1, PublicKey: "device-a", Address: "10.69.0.2"},
		{UserID: 1, PublicKey: "device-b", Address: "10.69.0.4"},
		{UserID: 2, PublicKey: "other", Address: "10.69.0.3"},
	}
	remove, err := planWireGuardUserPeerDelta(current, 1, "remove", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(remove.Remove) != 2 || len(remove.Set) != 0 || len(remove.Peers) != 1 || remove.Peers[0].PublicKey != "other" {
		t.Fatalf("remove delta = %+v, want both target peers removed and unrelated peer retained", remove)
	}
	update, err := planWireGuardUserPeerDelta(current, 1, "update", current[:2])
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Set) != 0 || len(update.Remove) != 0 {
		t.Fatalf("identical update = %+v, want no kernel changes", update)
	}
}

func TestPlanWireGuardUserMutationRejectsCrossUserOrDuplicatePeers(t *testing.T) {
	if _, err := planWireGuardUserPeerDelta(nil, 1, "add", []wireGuardRuntimePeer{{UserID: 2, PublicKey: "key"}}); err == nil {
		t.Fatal("expected cross-user peer to be rejected")
	}
	if _, err := planWireGuardUserPeerDelta(nil, 1, "add", []wireGuardRuntimePeer{{UserID: 1, PublicKey: "key"}, {UserID: 1, PublicKey: "key"}}); err == nil {
		t.Fatal("expected duplicate target key to be rejected")
	}
	if _, err := planWireGuardUserPeerDelta([]wireGuardRuntimePeer{{UserID: 2, PublicKey: "owned"}}, 1, "add", []wireGuardRuntimePeer{{UserID: 1, PublicKey: "owned"}}); err == nil {
		t.Fatal("expected a key already owned by another user to be rejected")
	}
	if _, err := planWireGuardUserPeerDelta(
		[]wireGuardRuntimePeer{{UserID: 2, PublicKey: "other", Address: "10.69.0.2"}},
		1,
		"add",
		[]wireGuardRuntimePeer{{UserID: 1, PublicKey: "new", Address: "10.69.0.2"}},
	); err == nil {
		t.Fatal("expected duplicate peer address to be rejected")
	}
}
