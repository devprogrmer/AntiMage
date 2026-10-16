package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// This runs beside the Controller-to-NodeAgent native test on the same live
// kernel WireGuard interface. It obstructs the real runtime-policy destination
// after the peer has changed, then proves the executor restores the kernel
// peer, config, and usage policy when the filesystem commit fails.
func TestWireGuardTargetedMutationPersistenceFailureNativeRollback(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_TARGETED_MUTATION_E2E") != "1" {
		t.Skip("requires the privileged native WireGuard driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native WireGuard rollback E2E requires Linux root")
	}
	iface := wireGuardRequiredEnv(t, "ANTIMAGE_WIREGUARD_NATIVE_INTERFACE")
	primaryKey := wireGuardRequiredEnv(t, "ANTIMAGE_WIREGUARD_NATIVE_PEER")
	serverPrivate := wireGuardRequiredEnv(t, "ANTIMAGE_WIREGUARD_NATIVE_SERVER_PRIVATE_KEY")
	namespace := wireGuardRequiredEnv(t, "ANTIMAGE_WIREGUARD_NATIVE_CLIENT_NS")
	before, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("ip", "link", "set", "dev", iface, "alias", wireGuardOwnershipAlias("native-wg")).Run(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, "node-data")
	appDir := filepath.Join(root, "node-app")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_NODE_APP_DIR", appDir)
	tag := "native-wg"
	hash := sha256.Sum256([]byte(tag))
	runtimeDir := filepath.Join(dataDir, "wireguard", "runtime", fmt.Sprintf("%x", hash[:8]))
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{
		"tag": tag, "interface_name": iface, "explicit_interface": true,
		"config_path": filepath.Join(runtimeDir, "wg.conf"), "server_cidr": "10.200.0.1/24", "source_cidr": "10.200.0.0/24",
	})
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	peer := wireGuardRuntimePeer{UserID: 7, PublicKey: primaryKey, Address: "10.200.0.2", Status: "active"}
	native, err := json.Marshal(nativeRuntimePayload{WireGuardInbounds: []wireGuardRuntimeInbound{{
		Tag: tag, ListenPort: 51820, Settings: map[string]any{
			"interface_name": iface, "private_key": serverPrivate, "address_pool": "10.200.0.0/24", "server_address": "10.200.0.1",
			"tproxy_enabled": false, "nat_enabled": false, "accounting_enabled": false,
		}, Peers: []wireGuardRuntimePeer{peer},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Config{DataDir: dataDir})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	initial := &nodev1.RuntimeConfigRequest{
		OperationId: "native-wg-rollback-initial", ConfigJson: `{"inbounds":[]}`,
		OvRuntimeJson: string(native), DesiredRevision: 1,
		Fence: &nodev1.DestructiveFence{OperationId: "native-wg-rollback-initial", CommandId: "native-wg-rollback-initial-command", ResourceId: "node-7", ResourceGeneration: 1, LeaseGeneration: 1},
	}
	if _, err := app.SyncConfig(ctx, initial); err != nil {
		t.Fatalf("initial native runtime sync: %v", err)
	}
	privateKeyOutput, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		t.Fatalf("generate target peer key: %v", err)
	}
	privateKey := strings.TrimSpace(string(privateKeyOutput))
	publicCommand := exec.Command("wg", "pubkey")
	publicCommand.Stdin = strings.NewReader(privateKey + "\n")
	publicKeyOutput, err := publicCommand.Output()
	if err != nil {
		t.Fatalf("derive target peer public key: %v", err)
	}
	publicKey := strings.TrimSpace(string(publicKeyOutput))
	target := wireGuardRuntimePeer{UserID: 8, PublicKey: publicKey, Address: "10.200.0.9", Status: "active"}
	user := &nodev1.InboundUser{Protocol: "wireguard", Fields: map[string]string{
		wireGuardMutationUserIDField: "8", wireGuardMutationOpField: "add",
		wireGuardMutationPeersField: mustNativeWireGuardJSON(t, []wireGuardRuntimePeer{{
			UserID: 8, PublicKey: target.PublicKey, PresharedKey: "", Address: target.Address, Status: target.Status,
		}}),
	}}
	// Keep the private key variable live in this E2E fixture to ensure the
	// generated public peer came from a valid keypair; the runtime stores only
	// the public key for this controlled add mutation.
	if strings.TrimSpace(privateKey) == "" {
		t.Fatal("generated WireGuard peer private key is empty")
	}
	policyPath := app.runtimePolicyPath()
	originalPolicy, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read initial committed runtime policy: %v", err)
	}
	backupPath := policyPath + ".native-e2e-backup"
	oldWriter := runtimePolicyAtomicWrite
	obstructed := false
	runtimePolicyAtomicWrite = func(path string, raw []byte) error {
		if path == policyPath && !obstructed {
			if err := os.Rename(path, backupPath); err != nil {
				return err
			}
			if err := os.Mkdir(path, 0700); err != nil {
				_ = os.Rename(backupPath, path)
				return err
			}
			obstructed = true
		}
		return writeAccountingState(path, raw)
	}
	defer func() {
		runtimePolicyAtomicWrite = oldWriter
		if obstructed {
			_ = os.Remove(policyPath)
			_ = os.Rename(backupPath, policyPath)
		}
	}()
	response, err := app.AddUser(ctx, &nodev1.InboundUserRequest{OperationId: "native-wg-persist-failure", InboundTag: tag, User: user})
	if err == nil || response != nil {
		t.Fatalf("expected real runtime-policy persistence failure, response=%v err=%v", response, err)
	}
	if !obstructed {
		t.Fatal("runtime-policy persistence obstruction was not reached after kernel mutation")
	}
	if nativeWireGuardPeerHasAddress(t, iface, target.Address) {
		t.Fatal("target peer remained in kernel after persistence failure rollback")
	}
	runtimeConfig, err := os.ReadFile(filepath.Join(runtimeDir, "wg.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(runtimeConfig), target.PublicKey) {
		t.Fatal("target peer remained in runtime config after persistence failure rollback")
	}
	if !nativeWireGuardPeerHasPublicKey(t, iface, primaryKey) {
		t.Fatal("rollback changed unrelated live peer")
	}
	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, policyPath); err != nil {
		t.Fatal(err)
	}
	restoredPolicy, err := os.ReadFile(policyPath)
	if err != nil || string(restoredPolicy) != string(originalPolicy) {
		t.Fatalf("runtime policy was not restored byte-for-byte: err=%v", err)
	}
	assertWireGuardNativeClientTraffic(t, namespace)
	after, err := net.InterfaceByName(iface)
	if err != nil || after.Index != before.Index {
		t.Fatalf("persistence rollback recreated interface: before=%d after=%v err=%v", before.Index, after, err)
	}
	t.Log("real runtime-policy filesystem obstruction after kernel Add caused exact target rollback; unrelated peer traffic and interface identity remained intact")
}

func mustNativeWireGuardJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func wireGuardRequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("required native E2E environment variable %s is empty", name)
	}
	return value
}

func nativeWireGuardPeerHasAddress(t *testing.T, iface, address string) bool {
	t.Helper()
	output, err := exec.Command("wg", "show", iface, "allowed-ips").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.Contains(fields[1], address+"/32") {
			return true
		}
	}
	return false
}

func nativeWireGuardPeerHasPublicKey(t *testing.T, iface, publicKey string) bool {
	t.Helper()
	output, err := exec.Command("wg", "show", iface, "peers").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(output), publicKey)
}

func assertWireGuardNativeClientTraffic(t *testing.T, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ip", "netns", "exec", namespace, "ping", "-c", "1", "-W", "2", "10.200.0.1").CombinedOutput()
	if err != nil {
		t.Fatalf("unrelated client traffic after rollback failed: %v: %s", err, output)
	}
}
