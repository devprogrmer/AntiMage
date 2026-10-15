package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type amneziaWGMutationHarness struct {
	server   *Server
	peers    map[string]wireGuardKernelPeer
	commands int
}

func newAmneziaWGMutationHarness(t *testing.T, initial []amneziaWGRuntimePeer) *amneziaWGMutationHarness {
	t.Helper()
	server := New(Config{DataDir: t.TempDir()})
	inbound := amneziaWGRuntimeInbound{
		Tag: "awg-mutation", ListenPort: 51829,
		Settings: map[string]any{
			"private_key": awgTestKey('s'), "address_pool": "10.72.0.0/24", "server_address": "10.72.0.1/24",
			"h1": "101", "h2": "102", "h3": "103", "h4": "104",
		},
		Peers: initial,
	}
	prepared, err := server.prepareAmneziaWGInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	state := amneziaWGRuntimeState{
		Tag: inbound.Tag, InterfaceName: prepared.InterfaceName, ConfigPath: prepared.ConfigPath,
		ServerCIDR: prepared.ServerCIDR, SourceCIDR: prepared.SourceCIDR, MTU: prepared.MTU,
	}
	server.amneziaWGRuntimes[inbound.Tag] = state
	if err := server.persistAmneziaWGRuntimeStates(); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(filepath.Dir(prepared.ConfigPath), "usage-helper.json")
	helperRaw, err := json.Marshal(prepared.UsageConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := amneziaWGDurableWrite(helperPath, helperRaw); err != nil {
		t.Fatal(err)
	}
	payloadRaw, err := json.Marshal(nativeRuntimePayload{AmneziaWGInbounds: []amneziaWGRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(payloadRaw), DesiredRevision: 5}); err != nil {
		t.Fatal(err)
	}
	h := &amneziaWGMutationHarness{server: server, peers: make(map[string]wireGuardKernelPeer)}
	for _, peer := range initial {
		h.peers[peer.PublicKey] = wireGuardKernelPeer{
			PublicKey: peer.PublicKey, PresharedKey: peer.PresharedKey, AllowedIPs: []string{peer.Address + "/32"},
		}
	}
	oldSnapshot, oldApply, oldIdentity, oldSnapshotAll := amneziaWGPeerSnapshot, amneziaWGPeerApply, amneziaWGInterfaceIdentity, amneziaWGSnapshotAll
	oldConfigWrite, oldDurableWrite := amneziaWGMutationWrite, amneziaWGMutationDurableWrite
	t.Cleanup(func() {
		amneziaWGPeerSnapshot, amneziaWGPeerApply = oldSnapshot, oldApply
		amneziaWGInterfaceIdentity, amneziaWGSnapshotAll = oldIdentity, oldSnapshotAll
		amneziaWGMutationWrite, amneziaWGMutationDurableWrite = oldConfigWrite, oldDurableWrite
	})
	amneziaWGInterfaceIdentity = func(string) (string, error) { return "boot:awg-29", nil }
	amneziaWGPeerSnapshot = func(string) (map[string]wireGuardKernelPeer, error) {
		out := make(map[string]wireGuardKernelPeer, len(h.peers))
		for key, peer := range h.peers {
			out[key] = peer
		}
		return out, nil
	}
	amneziaWGSnapshotAll = func(_ context.Context, names []string) (map[string][]wireGuardPeerCounters, error) {
		out := make(map[string][]wireGuardPeerCounters, len(names))
		for _, name := range names {
			for _, peer := range h.peers {
				out[name] = append(out[name], wireGuardPeerCounters{PublicKey: peer.PublicKey, Endpoint: peer.Endpoint, ReceivedBytes: peer.ReceivedBytes, SentBytes: peer.SentBytes})
			}
		}
		return out, nil
	}
	amneziaWGPeerApply = func(_ string, step wireGuardPeerMutationStep) error {
		h.commands++
		if step.Operation == "remove" {
			delete(h.peers, step.Peer.PublicKey)
			return nil
		}
		prefix, parseErr := netip.ParsePrefix(step.Peer.AllowedIPs[0])
		if parseErr != nil {
			return parseErr
		}
		h.peers[step.Peer.PublicKey] = wireGuardKernelPeer{
			PublicKey: step.Peer.PublicKey, PresharedKey: step.Peer.PresharedKey,
			AllowedIPs: []string{prefix.String()},
		}
		return nil
	}
	return h
}

func (h *amneziaWGMutationHarness) user(userID int64, operation string, peers []amneziaWGRuntimePeer) *nodev1.InboundUser {
	raw, _ := json.Marshal(peers)
	return &nodev1.InboundUser{Protocol: "amneziawg", Fields: map[string]string{
		amneziaWGMutationUserIDField: fmt.Sprint(userID),
		amneziaWGMutationOpField:     operation,
		amneziaWGMutationPeersField:  string(raw),
	}}
}

func (h *amneziaWGMutationHarness) helper(t *testing.T) amneziaWGUsageRuntimeConfig {
	t.Helper()
	path := filepath.Join(h.server.cfg.DataDir, "amneziawg", "runtime", amneziaWGRuntimeDirName("awg-mutation"), "usage-helper.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg amneziaWGUsageRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestApplyAmneziaWGUserMutationAddUpdateRemoveKeepsUnrelatedPeerAndInterface(t *testing.T) {
	otherKey, targetKey := awgTestKey('o'), awgTestKey('p')
	other := amneziaWGRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.72.0.8", Status: "active"}
	h := newAmneziaWGMutationHarness(t, []amneziaWGRuntimePeer{other})
	added := amneziaWGRuntimePeer{UserID: 7, PublicKey: targetKey, Address: "10.72.0.7", Status: "active"}
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "add", []amneziaWGRuntimePeer{added}), "add"); err != nil {
		t.Fatal(err)
	}
	updated := added
	updated.Address = "10.72.0.9"
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "update", []amneziaWGRuntimePeer{updated}), "update"); err != nil {
		t.Fatal(err)
	}
	if got := h.peers[targetKey].AllowedIPs[0]; got != "10.72.0.9/32" {
		t.Fatalf("updated AWG AllowedIPs=%q", got)
	}
	if got := h.helper(t); got.PeerGenerations[otherKey] == "" || got.Peers[otherKey] != 8 {
		t.Fatalf("unrelated AWG helper identity changed: %+v", got)
	}
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "remove", nil), "remove"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("removed AWG peer remains in device")
	}
	if got := h.peers[otherKey].AllowedIPs[0]; got != "10.72.0.8/32" {
		t.Fatalf("unrelated AWG peer changed: %q", got)
	}
	if h.commands != 3 {
		t.Fatalf("peer operations=%d, want three target-only changes", h.commands)
	}
	if identity, _ := amneziaWGInterfaceIdentity("awg-mutation"); identity != "boot:awg-29" {
		t.Fatalf("AmneziaWG interface identity changed: %q", identity)
	}
}

func TestApplyAmneziaWGUserMutationPolicyPersistenceFailureRollsBackTarget(t *testing.T) {
	targetKey := awgTestKey('p')
	h := newAmneziaWGMutationHarness(t, nil)
	oldWriter := runtimePolicyAtomicWrite
	failed := false
	runtimePolicyAtomicWrite = func(string, []byte) error {
		if !failed {
			failed = true
			return fmt.Errorf("injected policy persistence failure")
		}
		return nil
	}
	t.Cleanup(func() { runtimePolicyAtomicWrite = oldWriter })
	peer := amneziaWGRuntimePeer{UserID: 7, PublicKey: targetKey, Address: "10.72.0.7", Status: "active"}
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "add", []amneziaWGRuntimePeer{peer}), "add"); err == nil {
		t.Fatal("expected native policy persistence failure")
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("target AWG peer remained after rollback")
	}
	if got := h.helper(t); len(got.Peers) != 0 || got.Transitioning {
		t.Fatalf("helper policy after rollback=%+v", got)
	}
}

func TestApplyAmneziaWGUserMutationConfigWriteFailureRollsBackTarget(t *testing.T) {
	targetKey := awgTestKey('p')
	h := newAmneziaWGMutationHarness(t, nil)
	oldWriter := amneziaWGMutationWrite
	amneziaWGMutationWrite = func(string, []byte, os.FileMode) error {
		return fmt.Errorf("injected runtime config write failure")
	}
	t.Cleanup(func() { amneziaWGMutationWrite = oldWriter })
	peer := amneziaWGRuntimePeer{UserID: 7, PublicKey: targetKey, Address: "10.72.0.7", Status: "active"}
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "add", []amneziaWGRuntimePeer{peer}), "add"); err == nil {
		t.Fatal("expected runtime config persistence failure")
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("target AWG peer remained after config rollback")
	}
	if got := h.helper(t); len(got.Peers) != 0 || got.Transitioning {
		t.Fatalf("helper policy after config rollback=%+v", got)
	}
}

func TestApplyAmneziaWGIdenticalUpdateIsKernelNoop(t *testing.T) {
	peer := amneziaWGRuntimePeer{UserID: 7, PublicKey: awgTestKey('p'), Address: "10.72.0.7", Status: "active"}
	h := newAmneziaWGMutationHarness(t, []amneziaWGRuntimePeer{peer})
	if err := h.server.applyAmneziaWGUserMutation(context.Background(), "awg-mutation", h.user(7, "update", []amneziaWGRuntimePeer{peer}), "update"); err != nil {
		t.Fatal(err)
	}
	if h.commands != 0 {
		t.Fatalf("identical update issued %d kernel commands", h.commands)
	}
}

func TestAmneziaWGMutationRPCAddUpdateRemoveUsesLifecycleAndPreservesOtherPeer(t *testing.T) {
	otherKey, targetKey := awgTestKey('o'), awgTestKey('p')
	other := amneziaWGRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.72.0.8", Status: "active"}
	h := newAmneziaWGMutationHarness(t, []amneziaWGRuntimePeer{other})
	added := amneziaWGRuntimePeer{UserID: 7, PublicKey: targetKey, Address: "10.72.0.7", Status: "active"}
	add, err := h.server.AddUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "awg-add-7", InboundTag: "awg-mutation", User: h.user(7, "add", []amneziaWGRuntimePeer{added}),
	})
	if err != nil || !add.GetAccepted() || add.GetOperationId() != "awg-add-7" {
		t.Fatalf("AWG AddUser response=%v err=%v", add, err)
	}
	updated := added
	updated.Address = "10.72.0.9"
	update, err := h.server.UpdateUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "awg-update-7", InboundTag: "awg-mutation", User: h.user(7, "update", []amneziaWGRuntimePeer{updated}),
	})
	if err != nil || !update.GetAccepted() || h.peers[targetKey].AllowedIPs[0] != "10.72.0.9/32" {
		t.Fatalf("AWG UpdateUser response=%v peer=%+v err=%v", update, h.peers[targetKey], err)
	}
	remove, err := h.server.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{
		OperationId: "awg-remove-7", InboundTag: "awg-mutation", Email: amneziaWGRemoveUserPrefix + "7",
	})
	if err != nil || !remove.GetAccepted() || remove.GetOperationId() != "awg-remove-7" {
		t.Fatalf("AWG RemoveUser response=%v err=%v", remove, err)
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("AWG RemoveUser left target peer in runtime")
	}
	if got, ok := h.peers[otherKey]; !ok || got.AllowedIPs[0] != "10.72.0.8/32" {
		t.Fatalf("unrelated AWG peer changed: %+v", got)
	}
}

func TestAmneziaWGMutationRPCRejectsOperationMismatchBeforeRuntime(t *testing.T) {
	h := newAmneziaWGMutationHarness(t, nil)
	user := h.user(7, "update", []amneziaWGRuntimePeer{{UserID: 7, PublicKey: awgTestKey('p'), Address: "10.72.0.7", Status: "active"}})
	if response, err := h.server.AddUser(context.Background(), &nodev1.InboundUserRequest{InboundTag: "awg-mutation", User: user}); response != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("AddUser response=%v err=%v, want invalid argument", response, err)
	}
	if h.commands != 0 {
		t.Fatalf("invalid operation envelope issued %d kernel commands", h.commands)
	}
}
