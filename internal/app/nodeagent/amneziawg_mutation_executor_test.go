package nodeagent

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestAmneziaWGPeerMutationPlanChangesOnlyTargetPeer(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	current := []amneziaWGRuntimePeer{
		{UserID: 8, PublicKey: otherKey, Address: "10.72.0.8", Status: "active"},
		{UserID: 7, PublicKey: targetKey, Address: "10.72.0.7", Status: "active"},
	}
	desired := []amneziaWGRuntimePeer{{UserID: 7, PublicKey: targetKey, Address: "10.72.0.9", Status: "active"}}
	delta, err := planAmneziaWGUserPeerDelta(current, 7, "update", desired)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]wireGuardKernelPeer{
		otherKey:  {PublicKey: otherKey, AllowedIPs: []string{"10.72.0.8/32"}},
		targetKey: {PublicKey: targetKey, AllowedIPs: []string{"10.72.0.7/32"}},
	}
	plan, err := amneziaWGKernelPlan(delta, snapshot, 7)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]wireGuardKernelPeer{}
	for key, peer := range snapshot {
		state[key] = peer
	}
	oldApply := amneziaWGPeerApply
	amneziaWGPeerApply = func(_ string, step wireGuardPeerMutationStep) error {
		if step.Operation == "remove" {
			delete(state, step.Peer.PublicKey)
			return nil
		}
		prefix, parseErr := netip.ParsePrefix(step.Peer.AllowedIPs[0])
		if parseErr != nil {
			return parseErr
		}
		state[step.Peer.PublicKey] = wireGuardKernelPeer{PublicKey: step.Peer.PublicKey, PresharedKey: step.Peer.PresharedKey, AllowedIPs: []string{prefix.String()}}
		return nil
	}
	t.Cleanup(func() { amneziaWGPeerApply = oldApply })
	if err := executeAmneziaWGPeerMutationPlan(context.Background(), "awg-test", plan); err != nil {
		t.Fatal(err)
	}
	if got := state[targetKey].AllowedIPs[0]; got != "10.72.0.9/32" {
		t.Fatalf("target AllowedIPs=%q", got)
	}
	if got := state[otherKey].AllowedIPs[0]; got != "10.72.0.8/32" {
		t.Fatalf("unrelated peer changed: %+v", state[otherKey])
	}
}

func TestAmneziaWGPeerMutationFailureRollsBackExactTargetConfig(t *testing.T) {
	key := strings.Repeat("A", 43) + "="
	before := wireGuardKernelPeer{PublicKey: key, PresharedKey: strings.Repeat("C", 43) + "=", AllowedIPs: []string{"10.72.0.7/32"}}
	delta, err := planAmneziaWGUserPeerDelta(
		[]amneziaWGRuntimePeer{{UserID: 7, PublicKey: key, PresharedKey: before.PresharedKey, Address: "10.72.0.7", Status: "active"}},
		7, "update",
		[]amneziaWGRuntimePeer{{UserID: 7, PublicKey: key, PresharedKey: strings.Repeat("D", 43) + "=", Address: "10.72.0.9", Status: "active"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := amneziaWGKernelPlan(delta, map[string]wireGuardKernelPeer{key: before}, 7)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]wireGuardKernelPeer{key: before}
	oldApply := amneziaWGPeerApply
	failed := false
	amneziaWGPeerApply = func(_ string, step wireGuardPeerMutationStep) error {
		if step.Operation == "set" && !failed {
			failed = true
			state[key] = wireGuardKernelPeer{PublicKey: key, PresharedKey: step.Peer.PresharedKey, AllowedIPs: append([]string(nil), step.Peer.AllowedIPs...)}
			return errors.New("injected partial AWG mutation")
		}
		state[key] = wireGuardKernelPeer{PublicKey: key, PresharedKey: step.Peer.PresharedKey, AllowedIPs: append([]string(nil), step.Peer.AllowedIPs...)}
		return nil
	}
	t.Cleanup(func() { amneziaWGPeerApply = oldApply })
	if err := executeAmneziaWGPeerMutationPlan(context.Background(), "awg-test", plan); err == nil {
		t.Fatal("expected partial mutation error")
	}
	if got := state[key]; got.PresharedKey != before.PresharedKey || len(got.AllowedIPs) != 1 || got.AllowedIPs[0] != before.AllowedIPs[0] {
		t.Fatalf("rollback state=%+v, want prior configurable peer state %+v", got, before)
	}
}

func TestAmneziaWGPeerMutationCancellationRollsBackEarlierTargetSteps(t *testing.T) {
	keyA, keyB := strings.Repeat("A", 43)+"=", strings.Repeat("B", 43)+"="
	current := []amneziaWGRuntimePeer{
		{UserID: 7, PublicKey: keyA, Address: "10.72.0.7", Status: "active"},
		{UserID: 7, PublicKey: keyB, Address: "10.72.0.8", Status: "active"},
	}
	desired := []amneziaWGRuntimePeer{
		{UserID: 7, PublicKey: keyA, Address: "10.72.0.9", Status: "active"},
		{UserID: 7, PublicKey: keyB, Address: "10.72.0.10", Status: "active"},
	}
	delta, err := planAmneziaWGUserPeerDelta(current, 7, "update", desired)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]wireGuardKernelPeer{
		keyA: {PublicKey: keyA, AllowedIPs: []string{"10.72.0.7/32"}},
		keyB: {PublicKey: keyB, AllowedIPs: []string{"10.72.0.8/32"}},
	}
	plan, err := amneziaWGKernelPlan(delta, snapshot, 7)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]wireGuardKernelPeer{keyA: snapshot[keyA], keyB: snapshot[keyB]}
	ctx, cancel := context.WithCancel(context.Background())
	oldApply := amneziaWGPeerApply
	var calls int
	amneziaWGPeerApply = func(_ string, step wireGuardPeerMutationStep) error {
		calls++
		prefix, parseErr := netip.ParsePrefix(step.Peer.AllowedIPs[0])
		if parseErr != nil {
			return parseErr
		}
		state[step.Peer.PublicKey] = wireGuardKernelPeer{PublicKey: step.Peer.PublicKey, AllowedIPs: []string{prefix.String()}}
		if calls == 1 {
			cancel()
		}
		return nil
	}
	t.Cleanup(func() { amneziaWGPeerApply = oldApply; cancel() })
	if err := executeAmneziaWGPeerMutationPlan(ctx, "awg-test", plan); err == nil {
		t.Fatal("expected cancellation")
	}
	if got := state[keyA].AllowedIPs[0]; got != "10.72.0.7/32" {
		t.Fatalf("first peer after canceled transaction=%q", got)
	}
	if got := state[keyB].AllowedIPs[0]; got != "10.72.0.8/32" {
		t.Fatalf("second peer after canceled transaction=%q", got)
	}
}
