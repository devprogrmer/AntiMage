package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type mutationTestPeer struct {
	publicKey string
	psk       string
	endpoint  string
	allowed   string
	keepalive string
	rx        string
	tx        string
}

type wireGuardMutationHarness struct {
	server   *Server
	peers    map[string]mutationTestPeer
	commands [][]string
}

func newWireGuardMutationHarness(t *testing.T, initial []wireGuardRuntimePeer) *wireGuardMutationHarness {
	t.Helper()
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})
	settings := map[string]any{
		"interface_name": "wg0", "private_key": strings.Repeat("D", 43) + "=",
		"address_pool": "10.69.0.0/24", "server_address": "10.69.0.1",
	}
	inbound := wireGuardRuntimeInbound{Tag: "wg-main", ListenPort: 51820, Settings: settings, Peers: initial}
	prepared, err := server.prepareWireGuardInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.syncWireGuardUsageConfigs([]wireGuardRuntimeInbound{inbound}); err != nil {
		t.Fatal(err)
	}
	state := wireGuardRuntimeState{
		Tag: "wg-main", InterfaceName: "wg0", ExplicitInterface: true, ConfigPath: prepared.ConfigPath,
		ServerCIDR: prepared.ServerCIDR, SourceCIDR: prepared.SourceCIDR,
	}
	server.wireGuardRuntimes["wg-main"] = state
	if err := server.persistWireGuardRuntimeState(state); err != nil {
		t.Fatal(err)
	}
	runtimePayload := nativeRuntimePayload{WireGuardInbounds: []wireGuardRuntimeInbound{inbound}}
	rawNative, err := json.Marshal(runtimePayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(rawNative), DesiredRevision: 4,
	}); err != nil {
		t.Fatal(err)
	}
	h := &wireGuardMutationHarness{server: server, peers: make(map[string]mutationTestPeer)}
	for _, peer := range initial {
		h.peers[peer.PublicKey] = mutationTestPeer{
			publicKey: peer.PublicKey, psk: peer.PresharedKey,
			allowed: strings.TrimSpace(peer.Address) + "/32", keepalive: "0", rx: "0", tx: "0",
		}
	}
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldDump := wireGuardDumpInterface
	oldIdentity := wireGuardInterfaceIdentity
	oldConfigWrite := wireGuardMutationConfigWrite
	oldReadFile := wireGuardRuntimeReadFile
	t.Cleanup(func() {
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardDumpInterface = oldDump
		wireGuardInterfaceIdentity = oldIdentity
		wireGuardMutationConfigWrite = oldConfigWrite
		wireGuardRuntimeReadFile = oldReadFile
	})
	wireGuardRuntimeLookPath = func(name string) (string, error) { return "/fake/" + name, nil }
	wireGuardInterfaceIdentity = func(string) (string, error) { return "boot:77", nil }
	wireGuardRuntimeReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, string(os.PathSeparator)+"ifalias") {
			return []byte(wireGuardOwnershipAlias("wg-main")), nil
		}
		return os.ReadFile(path)
	}
	wireGuardDumpInterface = func(context.Context, string) ([]byte, error) {
		raw := h.dump()
		return []byte(raw), nil
	}
	wireGuardRuntimeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		all := append([]string{name}, args...)
		h.commands = append(h.commands, all)
		if name == "/fake/ip" && len(args) >= 3 && args[0] == "link" && args[1] == "show" {
			return []byte("2: wg0: <UP>"), nil
		}
		if name != "/fake/wg" || len(args) == 0 {
			return nil, fmt.Errorf("unexpected command %v", all)
		}
		if args[0] == "show" && len(args) == 3 && args[2] == "peers" {
			keys := make([]string, 0, len(h.peers))
			for key := range h.peers {
				keys = append(keys, key)
			}
			return []byte(strings.Join(keys, "\n") + "\n"), nil
		}
		if args[0] != "set" || len(args) < 5 || args[2] != "peer" {
			return nil, fmt.Errorf("unexpected WireGuard command %v", all)
		}
		key := args[3]
		if args[4] == "remove" {
			delete(h.peers, key)
			return nil, nil
		}
		peer := h.peers[key]
		peer.publicKey = key
		if peer.rx == "" {
			peer.rx = "0"
		}
		if peer.tx == "" {
			peer.tx = "0"
		}
		if peer.keepalive == "" {
			peer.keepalive = "0"
		}
		for i := 4; i+1 < len(args); i += 2 {
			switch args[i] {
			case "preshared-key":
				if args[i+1] == "/dev/null" {
					peer.psk = ""
				} else {
					raw, readErr := os.ReadFile(args[i+1])
					if readErr != nil {
						return nil, readErr
					}
					peer.psk = strings.TrimSpace(string(raw))
				}
			case "allowed-ips":
				peer.allowed = args[i+1]
			case "endpoint":
				peer.endpoint = args[i+1]
			case "persistent-keepalive":
				peer.keepalive = args[i+1]
			}
		}
		h.peers[key] = peer
		return nil, nil
	}
	return h
}

func (h *wireGuardMutationHarness) dump() string {
	var b strings.Builder
	b.WriteString("private\tpublic\t51820\toff\n")
	for _, peer := range h.peers {
		psk := peer.psk
		if psk == "" {
			psk = "(none)"
		}
		endpoint := peer.endpoint
		if endpoint == "" {
			endpoint = "(none)"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t0\t%s\t%s\t%s\n", peer.publicKey, psk, endpoint, peer.allowed, peer.rx, peer.tx, peer.keepalive)
	}
	return b.String()
}

func (h *wireGuardMutationHarness) user(id int64, operation string, peers []wireGuardRuntimePeer) *nodev1.InboundUser {
	raw, _ := json.Marshal(peers)
	return &nodev1.InboundUser{Protocol: "wireguard", Fields: map[string]string{
		wireGuardMutationUserIDField: fmt.Sprint(id), wireGuardMutationOpField: operation,
		wireGuardMutationPeersField: string(raw),
	}}
}

func (h *wireGuardMutationHarness) linkMutations() int {
	count := 0
	for _, args := range h.commands {
		if len(args) >= 3 && args[0] == "/fake/ip" && args[1] == "link" && (args[2] == "add" || args[2] == "delete") {
			count++
		}
	}
	return count
}

func TestApplyWireGuardUserMutationAddUpdateRemoveKeepsInterfaceAndOtherPeer(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	other := wireGuardRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.69.0.8", Status: "active"}
	h := newWireGuardMutationHarness(t, []wireGuardRuntimePeer{other})
	if _, err := parseWireGuardDump(h.dump()); err != nil {
		t.Fatalf("test kernel dump is invalid: %v: %q", err, h.dump())
	}
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", h.user(7, "add", []wireGuardRuntimePeer{{UserID: 7, PublicKey: targetKey, Address: "10.69.0.7", Status: "active"}}), "add"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.peers[targetKey]; !ok {
		t.Fatal("added peer not present")
	}
	updated := wireGuardRuntimePeer{UserID: 7, PublicKey: targetKey, Address: "10.69.0.9", Status: "active"}
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", h.user(7, "update", []wireGuardRuntimePeer{updated}), "update"); err != nil {
		t.Fatal(err)
	}
	if got := h.peers[targetKey].allowed; got != "10.69.0.9/32" {
		t.Fatalf("updated AllowedIPs = %q", got)
	}
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", h.user(7, "remove", nil), "remove"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("removed peer still present")
	}
	if got, ok := h.peers[otherKey]; !ok || got.allowed != "10.69.0.8/32" {
		t.Fatalf("unrelated peer changed: %+v", got)
	}
	if h.linkMutations() != 0 {
		t.Fatal("peer mutation recreated WireGuard interface")
	}
	if got, err := wireGuardInterfaceIdentity("wg0"); err != nil || got != "boot:77" {
		t.Fatalf("interface identity = %q, err=%v", got, err)
	}
}

func TestApplyWireGuardUserMutationPersistenceFailureRollsBackPeerState(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	other := wireGuardRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.69.0.8", Status: "active"}
	h := newWireGuardMutationHarness(t, []wireGuardRuntimePeer{other})
	oldWriter := runtimePolicyAtomicWrite
	called := false
	runtimePolicyAtomicWrite = func(string, []byte) error {
		if !called {
			called = true
			return errors.New("injected policy disk failure")
		}
		return nil
	}
	t.Cleanup(func() { runtimePolicyAtomicWrite = oldWriter })
	user := h.user(7, "add", []wireGuardRuntimePeer{{UserID: 7, PublicKey: targetKey, Address: "10.69.0.7", Status: "active"}})
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", user, "add"); err == nil {
		t.Fatal("expected policy persistence failure")
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("target peer remained after failed transaction")
	}
	if got, ok := h.peers[otherKey]; !ok || got.allowed != "10.69.0.8/32" {
		t.Fatalf("unrelated peer changed during rollback: %+v", got)
	}
	if h.linkMutations() != 0 {
		t.Fatal("rollback recreated WireGuard interface")
	}
	policy, err := h.server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(policy.NativeJSON, targetKey) {
		t.Fatal("failed mutation was published to runtime policy")
	}
}

func TestApplyWireGuardUserMutationHelperFailureRestoresExactTargetSnapshot(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	oldPSK := strings.Repeat("C", 43) + "="
	newPSK := strings.Repeat("D", 43) + "="
	other := wireGuardRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.69.0.8", Status: "active"}
	target := wireGuardRuntimePeer{UserID: 7, PublicKey: targetKey, PresharedKey: oldPSK, Address: "10.69.0.7", Status: "active"}
	h := newWireGuardMutationHarness(t, []wireGuardRuntimePeer{other, target})
	before := mutationTestPeer{publicKey: targetKey, psk: oldPSK, endpoint: "198.51.100.7:51820", allowed: "10.69.0.7/32", keepalive: "25", rx: "100", tx: "200"}
	h.peers[targetKey] = before
	oldWriter := wireGuardUsageConfigWrite
	called := false
	wireGuardUsageConfigWrite = func(path string, raw []byte, mode os.FileMode) error {
		if !called {
			called = true
			return errors.New("injected helper policy failure")
		}
		return atomicWriteFile(path, raw, mode)
	}
	t.Cleanup(func() { wireGuardUsageConfigWrite = oldWriter })
	want := target
	want.Address, want.PresharedKey = "10.69.0.9", newPSK
	user := h.user(7, "update", []wireGuardRuntimePeer{want})
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", user, "update"); err == nil {
		t.Fatal("expected usage helper persistence failure")
	}
	if got := h.peers[targetKey]; got != before {
		t.Fatalf("target configurable state after rollback = %+v, want %+v", got, before)
	}
	if got := h.peers[otherKey]; got.allowed != "10.69.0.8/32" {
		t.Fatalf("unrelated peer changed: %+v", got)
	}
	if h.linkMutations() != 0 {
		t.Fatal("rollback recreated WireGuard interface")
	}
}

func TestApplyWireGuardUserMutationRejectsUnexpectedKernelPeer(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	foreignKey := strings.Repeat("E", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	other := wireGuardRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.69.0.8", Status: "active"}
	h := newWireGuardMutationHarness(t, []wireGuardRuntimePeer{other})
	h.peers[foreignKey] = mutationTestPeer{publicKey: foreignKey, allowed: "10.69.0.9/32", keepalive: "0", rx: "0", tx: "0"}
	user := h.user(7, "add", []wireGuardRuntimePeer{{UserID: 7, PublicKey: targetKey, Address: "10.69.0.7", Status: "active"}})
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", user, "add"); err == nil {
		t.Fatal("accepted a kernel peer absent from committed policy")
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("kernel changed after stale inventory was rejected")
	}
	for _, args := range h.commands {
		if len(args) > 1 && args[0] == "/fake/wg" && args[1] == "set" {
			t.Fatalf("kernel mutation ran with stale inventory: %v", args)
		}
	}
}

func TestApplyWireGuardUserMutationCheckpointFailureDoesNotTouchKernel(t *testing.T) {
	otherKey := strings.Repeat("B", 43) + "="
	targetKey := strings.Repeat("A", 43) + "="
	other := wireGuardRuntimePeer{UserID: 8, PublicKey: otherKey, Address: "10.69.0.8", Status: "active"}
	h := newWireGuardMutationHarness(t, []wireGuardRuntimePeer{other})
	oldWrite := wireGuardAccountingWrite
	wireGuardAccountingWrite = func(string, []byte) error { return errors.New("injected accounting checkpoint failure") }
	t.Cleanup(func() { wireGuardAccountingWrite = oldWrite })
	user := h.user(7, "add", []wireGuardRuntimePeer{{UserID: 7, PublicKey: targetKey, Address: "10.69.0.7", Status: "active"}})
	if err := h.server.applyWireGuardUserMutation(context.Background(), "wg-main", user, "add"); err == nil {
		t.Fatal("expected accounting checkpoint failure")
	}
	if _, ok := h.peers[targetKey]; ok {
		t.Fatal("kernel changed despite checkpoint failure")
	}
	for _, args := range h.commands {
		if len(args) > 1 && args[0] == "/fake/wg" && args[1] == "set" {
			t.Fatalf("kernel mutation ran before checkpoint: %v", args)
		}
	}
}
