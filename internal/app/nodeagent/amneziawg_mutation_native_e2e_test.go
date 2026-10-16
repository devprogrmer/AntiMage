//go:build linux

package nodeagent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The privileged AWG harness calls this while its real client tunnel is live.
// It exercises the NodeAgent RPC against the production Linux wgctrl executor.
func TestAmneziaWGTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_AWG_TARGETED_MUTATION_E2E") != "1" || os.Geteuid() != 0 {
		t.Skip("requires privileged native AmneziaWG driver")
	}
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_NATIVE_STATE"))
	iface := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_NATIVE_INTERFACE"))
	clientNS := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_NATIVE_CLIENT_NS"))
	serverPrivate := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SERVER_PRIVATE"))
	primaryKey := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_CLIENT_PUBLIC"))
	for name, value := range map[string]string{"state": stateDir, "interface": iface, "client namespace": clientNS, "server private key": serverPrivate, "primary public key": primaryKey} {
		if value == "" {
			t.Fatalf("native AmneziaWG %s is required", name)
		}
	}
	before, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("ip", "link", "set", "dev", iface, "alias", "antimage:native-awg").Run(); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{
		"private_key": serverPrivate, "address_pool": "10.74.0.0/24", "server_address": "10.74.0.1/24", "mtu": 1420,
		"jc": 4, "jmin": 8, "jmax": 80, "s1": 77, "s2": 90, "h1": "12345", "h2": "23456", "h3": "34567", "h4": "45678",
	}
	primary := amneziaWGRuntimePeer{UserID: 7, Username: "native-awg", DeviceIndex: 1, PublicKey: primaryKey, Address: "10.74.0.2", Status: "active", UsageCoefficient: 1.5, InboundCoefficient: 2}
	inbound := amneziaWGRuntimeInbound{Tag: "native-awg", TunnelTag: "native-awg-tunnel", ListenPort: 51821, Settings: settings, Peers: []amneziaWGRuntimePeer{primary}}
	nativeRaw, err := json.Marshal(nativeRuntimePayload{AmneziaWGInbounds: []amneziaWGRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Config{DataDir: stateDir})
	policyReq := &nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(nativeRaw), DesiredRevision: 1}
	if err := app.persistRuntimePolicy(policyReq); err != nil {
		t.Fatalf("seed current AWG desired policy: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	targetPrivate, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		t.Fatalf("generate target device key: %v", err)
	}
	public := exec.Command("wg", "pubkey")
	public.Stdin = strings.NewReader(strings.TrimSpace(string(targetPrivate)) + "\n")
	targetPublic, err := public.Output()
	if err != nil {
		t.Fatalf("derive target device public key: %v", err)
	}
	target := amneziaWGRuntimePeer{UserID: 8, Username: "native-target", DeviceIndex: 1, PublicKey: strings.TrimSpace(string(targetPublic)), Address: "10.74.0.9", Status: "active"}
	mutation := func(operation string, peer *amneziaWGRuntimePeer) *nodev1.InboundUser {
		peers := []amneziaWGRuntimePeer{}
		if peer != nil {
			peers = append(peers, *peer)
		}
		raw, marshalErr := json.Marshal(peers)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return &nodev1.InboundUser{Protocol: "amneziawg", Fields: map[string]string{
			amneziaWGMutationUserIDField: "8", amneziaWGMutationOpField: operation, amneziaWGMutationPeersField: string(raw),
		}}
	}
	add := &nodev1.InboundUserRequest{OperationId: "native-awg-add-8", InboundTag: "native-awg", User: mutation("add", &target)}
	previousPeerApply := amneziaWGPeerApply
	peerMutations := 0
	amneziaWGPeerApply = func(name string, step wireGuardPeerMutationStep) error {
		peerMutations++
		return previousPeerApply(name, step)
	}
	defer func() { amneziaWGPeerApply = previousPeerApply }()
	response, err := app.AddUser(ctx, add)
	if err != nil || response == nil || !response.GetAccepted() || response.GetOperationId() != add.GetOperationId() {
		t.Fatalf("native AddUser response=%v err=%v", response, err)
	}
	identity := nativeAmneziaWGPeerIdentity(t, iface)
	assertNativeAmneziaWGPeer(t, iface, target.PublicKey, "10.74.0.9/32", true)
	mutations := peerMutations
	response, err = app.AddUser(ctx, add)
	if err != nil || response == nil || !response.GetAccepted() || peerMutations != mutations {
		t.Fatalf("same-operation retry was not idempotent: response=%v mutations=%d want=%d err=%v", response, peerMutations, mutations, err)
	}
	conflict := &nodev1.InboundUserRequest{OperationId: add.GetOperationId(), InboundTag: add.GetInboundTag(), User: mutation("update", &target)}
	if response, err := app.UpdateUser(ctx, conflict); response != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("conflicting operation ID response=%v err=%v, want failed precondition", response, err)
	}
	if peerMutations != mutations {
		t.Fatal("conflicting operation ID mutated live peer state")
	}
	updated := target
	updated.Address = "10.74.0.10"
	update := &nodev1.InboundUserRequest{OperationId: "native-awg-update-8", InboundTag: "native-awg", User: mutation("update", &updated)}
	if response, err := app.UpdateUser(ctx, update); err != nil || response == nil || !response.GetAccepted() || response.GetOperationId() != update.GetOperationId() {
		t.Fatalf("native UpdateUser response=%v err=%v", response, err)
	}
	assertNativeAmneziaWGPeer(t, iface, target.PublicKey, "10.74.0.10/32", true)
	remove := &nodev1.RemoveInboundUserRequest{OperationId: "native-awg-remove-8", InboundTag: "native-awg", Email: amneziaWGRemoveUserPrefix + "8"}
	if response, err := app.RemoveUser(ctx, remove); err != nil || response == nil || !response.GetAccepted() || response.GetOperationId() != remove.GetOperationId() {
		t.Fatalf("native RemoveUser response=%v err=%v", response, err)
	}
	assertNativeAmneziaWGPeer(t, iface, target.PublicKey, "", false)
	assertNativeAmneziaWGPeer(t, iface, primaryKey, "10.74.0.2/32", true)
	assertNativeAmneziaWGClientTraffic(t, clientNS)
	after, err := net.InterfaceByName(iface)
	if err != nil || after.Index != before.Index || nativeAmneziaWGPeerIdentity(t, iface) != identity {
		t.Fatalf("AmneziaWG interface identity changed: before=%d after=%v err=%v", before.Index, after, err)
	}
	assertNativeAmneziaWGPersistenceRollback(t, app, ctx, inbound, target, clientNS, iface, primaryKey, before.Index)
	t.Log("real AmneziaWG Add/Update/Remove RPCs applied peer-only changes; duplicate operation retry did not reapply; conflicting ID was rejected; unrelated client traffic and interface identity remained intact; native persistence rollback passed")
}

func assertNativeAmneziaWGPersistenceRollback(t *testing.T, app *Server, ctx context.Context, inbound amneziaWGRuntimeInbound, target amneziaWGRuntimePeer, namespace, iface, primaryKey string, interfaceIndex int) {
	t.Helper()
	policyPath := app.runtimePolicyPath()
	original, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	backup := policyPath + ".native-e2e-backup"
	previous := runtimePolicyAtomicWrite
	obstructed := false
	runtimePolicyAtomicWrite = func(path string, raw []byte) error {
		if path == policyPath && !obstructed {
			if err := os.Rename(path, backup); err != nil {
				return err
			}
			if err := os.Mkdir(path, 0700); err != nil {
				_ = os.Rename(backup, path)
				return err
			}
			obstructed = true
		}
		return writeAccountingState(path, raw)
	}
	defer func() {
		runtimePolicyAtomicWrite = previous
		if obstructed {
			_ = os.Remove(policyPath)
			_ = os.Rename(backup, policyPath)
		}
	}()
	requestUser := &nodev1.InboundUser{Protocol: "amneziawg", Fields: map[string]string{
		amneziaWGMutationUserIDField: "8", amneziaWGMutationOpField: "add",
		amneziaWGMutationPeersField: mustAmneziaWGNativeJSON(t, []amneziaWGRuntimePeer{target}),
	}}
	response, err := app.AddUser(ctx, &nodev1.InboundUserRequest{OperationId: "native-awg-persist-failure", InboundTag: inbound.Tag, User: requestUser})
	if err == nil || response != nil || !obstructed {
		t.Fatalf("expected actual policy filesystem failure after peer mutation, response=%v err=%v obstructed=%v", response, err, obstructed)
	}
	assertNativeAmneziaWGPeer(t, iface, target.PublicKey, "", false)
	assertNativeAmneziaWGPeer(t, iface, primaryKey, "10.74.0.2/32", true)
	assertNativeAmneziaWGClientTraffic(t, namespace)
	if got, err := net.InterfaceByName(iface); err != nil || got.Index != interfaceIndex {
		t.Fatalf("rollback recreated AmneziaWG interface: got=%v err=%v want index=%d", got, err, interfaceIndex)
	}
	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, policyPath); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(policyPath)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("AmneziaWG runtime policy rollback was not exact: err=%v", err)
	}
}

func mustAmneziaWGNativeJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertNativeAmneziaWGPeer(t *testing.T, iface, key, allowedIP string, wantPresent bool) {
	t.Helper()
	snapshot, err := amneziaWGPeerSnapshot(iface)
	if err != nil {
		t.Fatal(err)
	}
	peer, exists := snapshot[key]
	if exists != wantPresent {
		t.Fatalf("native AmneziaWG peer %s present=%v want=%v", key, exists, wantPresent)
	}
	if exists && allowedIP != "" && (len(peer.AllowedIPs) != 1 || peer.AllowedIPs[0] != allowedIP) {
		t.Fatalf("native AmneziaWG peer AllowedIPs=%v want %s", peer.AllowedIPs, allowedIP)
	}
}

func nativeAmneziaWGPeerIdentity(t *testing.T, iface string) string {
	t.Helper()
	identity, err := amneziaWGInterfaceIdentity(iface)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func assertNativeAmneziaWGClientTraffic(t *testing.T, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ip", "netns", "exec", namespace, "ping", "-c", "1", "-W", "2", "10.74.0.1").CombinedOutput()
	if err != nil {
		t.Fatalf("unrelated AmneziaWG client traffic failed: %v: %s", err, output)
	}
}
