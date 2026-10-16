package nodecontroller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodeagent"
	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

// This privileged test is invoked by the native WireGuard driver while a
// separate client is actively exchanging traffic with the real kernel device.
// It calls the production Controller executor over TLS/gRPC and the production
// NodeAgent executor against that device. The capability remains intentionally
// unadvertised until this gate and the remaining rollback gates pass.
func TestControllerWireGuardTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_TARGETED_MUTATION_E2E") != "1" {
		t.Skip("requires the privileged native WireGuard driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native WireGuard targeted E2E requires Linux root")
	}
	iface := envRequired(t, "ANTIMAGE_WIREGUARD_NATIVE_INTERFACE")
	primaryKey := envRequired(t, "ANTIMAGE_WIREGUARD_NATIVE_PEER")
	serverPrivate := envRequired(t, "ANTIMAGE_WIREGUARD_NATIVE_SERVER_PRIVATE_KEY")
	clientNS := envRequired(t, "ANTIMAGE_WIREGUARD_NATIVE_CLIENT_NS")
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("native WireGuard E2E requires /dev/net/tun: %v", err)
	}
	if output, err := exec.Command("wg", "show", iface, "peers").CombinedOutput(); err != nil || !strings.Contains(string(output), primaryKey) {
		t.Fatalf("driver's active client peer is not present: peers=%s err=%v", output, err)
	}
	if err := exec.Command("ip", "link", "set", "dev", iface, "alias", "antimage:native-wg").Run(); err != nil {
		t.Fatalf("assign managed test interface ownership: %v", err)
	}
	before, err := net.InterfaceByName(iface)
	if err != nil {
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
	runtimeHash := sha256.Sum256([]byte(tag))
	runtimeDir := filepath.Join(dataDir, "wireguard", "runtime", fmt.Sprintf("%x", runtimeHash[:8]))
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	serverTLS, clientTLS := openVPNNativeControllerTLS(t)
	db, repo, node, xrayConfig, nativeConfig := wireGuardNativeControllerDB(t, ctx, root, iface, primaryKey, serverPrivate)
	defer db.Close()
	agent := nodeagent.New(nodeagent.Config{DataDir: dataDir})
	hello, err := agent.Hello(ctx, &nodev1.HelloRequest{MasterId: "native-wg-master"})
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range hello.GetRuntime().GetCapabilities() {
		if strings.Contains(capability, "wireguard") && strings.Contains(capability, "targeted_user") {
			t.Fatalf("WireGuard targeted capability advertised before native gate: %s", capability)
		}
	}
	var syncConfigCalls atomic.Int32
	var dropAck atomic.Bool
	var interceptor grpc.UnaryServerInterceptor
	interceptor = func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncConfigCalls.Add(1)
		}
		response, callErr := handler(ctx, req)
		if callErr == nil && dropAck.Load() {
			if mutation, ok := req.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == "add_user-301-native-wg" && dropAck.CompareAndSwap(true, false) {
				return nil, status.Error(codes.Unavailable, "simulated ACK loss after WireGuard commit")
			}
		}
		return response, callErr
	}
	listener, grpcServer, address := startOpenVPNNativeControllerAgentWithInterceptor(t, agent, serverTLS, interceptor)
	defer func() {
		grpcServer.Stop()
		_ = listener.Close()
	}()
	client, err := nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	initial := &nodev1.RuntimeConfigRequest{
		OperationId: "native-wg-initial", ConfigJson: xrayConfig,
		OvRuntimeJson: nativeConfig, DesiredRevision: 1,
		Fence: &nodev1.DestructiveFence{OperationId: "native-wg-initial", CommandId: "native-wg-initial-command", ResourceId: "node-7", ResourceGeneration: 1, LeaseGeneration: 1},
	}
	if _, err := client.Runtime().SyncConfig(ctx, initial); err != nil {
		t.Fatalf("initial real WireGuard runtime sync: %v", err)
	}
	if got := syncConfigCalls.Load(); got != 1 {
		t.Fatalf("initial sync count=%d, want 1", got)
	}
	assertNativeWireGuardClient(t, clientNS)
	initialHandshake := nativeWireGuardHandshake(t, iface, primaryKey)

	controller := NewController(repo)
	if _, err := db.ExecContext(ctx, `INSERT INTO users VALUES (8,'target-user',10,'active','1123456789abcdef0123456789abcdef',0,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO proxies VALUES (8,'wireguard')`); err != nil {
		t.Fatal(err)
	}
	dropAck.Store(true)
	add := OperationRow{ID: 301, OperationType: "add_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	if err := controller.grpcApplyWireGuardUserOperation(ctx, client, node, add); err == nil {
		t.Fatal("simulated lost ACK was reported as delivered")
	}
	if err := controller.grpcApplyWireGuardUserOperation(ctx, client, node, add); err != nil {
		t.Fatalf("same operation retry after lost ACK: %v", err)
	}
	var targetAddress string
	if err := db.QueryRowContext(ctx, `SELECT address FROM wireguard_devices WHERE inbound_tag=? AND user_id=8 AND device_index=0`, tag).Scan(&targetAddress); err != nil {
		t.Fatal(err)
	}
	if !nativeWireGuardPeerPresent(t, iface, targetAddress) {
		t.Fatalf("AddUser target address %s missing from live kernel peer", targetAddress)
	}
	var targetKey, updatedAddress string
	if err := db.QueryRowContext(ctx, `SELECT public_key,address FROM wireguard_devices WHERE inbound_tag=? AND user_id=8 AND device_index=0`, tag).Scan(&targetKey, &updatedAddress); err != nil {
		t.Fatal(err)
	}
	updatedAddress = "10.200.0.9"
	if _, err := db.ExecContext(ctx, `UPDATE wireguard_devices SET address=? WHERE inbound_tag=? AND user_id=8 AND device_index=0`, updatedAddress, tag); err != nil {
		t.Fatal(err)
	}
	update := OperationRow{ID: 302, OperationType: "update_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	if err := controller.grpcApplyWireGuardUserOperation(ctx, client, node, update); err != nil {
		t.Fatalf("UpdateUser targeted RPC: %v", err)
	}
	if !nativeWireGuardPeerPresent(t, iface, updatedAddress) {
		t.Fatalf("UpdateUser target address %s missing from live kernel peer", updatedAddress)
	}
	remove := OperationRow{ID: 303, OperationType: "remove_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	if err := controller.grpcApplyWireGuardUserOperation(ctx, client, node, remove); err != nil {
		t.Fatalf("RemoveUser targeted RPC: %v", err)
	}
	if nativeWireGuardPeerPresent(t, iface, updatedAddress) {
		t.Fatalf("RemoveUser left target key %s in the kernel", targetKey)
	}
	if got := syncConfigCalls.Load(); got != 1 {
		t.Fatalf("targeted mutations fell back to SyncConfig: sync calls=%d, want only initial sync", got)
	}
	assertNativeWireGuardClient(t, clientNS)
	if got := nativeWireGuardHandshake(t, iface, primaryKey); got < initialHandshake {
		t.Fatalf("unrelated client handshake regressed: before=%d after=%d", initialHandshake, got)
	}
	after, err := net.InterfaceByName(iface)
	if err != nil || after.Index != before.Index {
		t.Fatalf("WireGuard interface identity changed: before=%d after=%v err=%v", before.Index, after, err)
	}
	t.Log("real Controller -> TLS/gRPC NodeAgent -> kernel Add/Update/Remove passed; ACK-loss retry was idempotent; client traffic, peer handshake and interface identity were preserved; no targeted-path SyncConfig fallback")
}

func wireGuardNativeControllerDB(t *testing.T, ctx context.Context, root, iface, primaryKey, serverPrivate string) (*sql.DB, Repository, NodeRow, string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "wireguard-controller.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	ddl := `
CREATE TABLE users (id INTEGER PRIMARY KEY,username TEXT,service_id INTEGER,status TEXT,credential_key TEXT,used_traffic INTEGER DEFAULT 0,data_limit INTEGER,expire INTEGER,device_limit INTEGER DEFAULT 0,ip_limit INTEGER DEFAULT 0,upload_speed_limit INTEGER DEFAULT 0,download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER,type TEXT);
CREATE TABLE service_hosts (service_id INTEGER,host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY,inbound_tag TEXT,is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY,data TEXT);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY,uplink INTEGER DEFAULT 0,downlink INTEGER DEFAULT 0,usage_coefficient REAL DEFAULT 1);
CREATE TABLE nodes (id INTEGER PRIMARY KEY,name TEXT,address TEXT,port INTEGER,api_port INTEGER,status TEXT,xray_version TEXT,message TEXT,certificate TEXT,certificate_key TEXT,xray_config_mode TEXT,xray_config TEXT,usage_coefficient REAL DEFAULT 1);
CREATE TABLE wireguard_devices (inbound_tag TEXT,user_id INTEGER,device_index INTEGER,private_key TEXT,public_key TEXT,address TEXT,generation INTEGER DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,public_key),UNIQUE(inbound_tag,address));
CREATE TABLE wireguard_device_revocations (inbound_tag TEXT,user_id INTEGER,device_index INTEGER,public_key TEXT,credential_fingerprint TEXT);
CREATE TABLE node_wireguard_usage_reflection (node_id INTEGER,user_id INTEGER,batch_id TEXT);
`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	configValue := map[string]any{"inbounds": []any{map[string]any{"tag": "native-wg", "protocol": "wireguard", "port": 51820, "targets": []any{"node:7"}, "settings": map[string]any{"interface_name": iface, "private_key": serverPrivate, "address_pool": "10.200.0.0/24", "server_address": "10.200.0.1", "tproxy_enabled": false, "nat_enabled": false, "accounting_enabled": false}}}}
	raw, err := json.Marshal(configValue)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO xray_config VALUES (1,?); INSERT INTO inbounds VALUES ('native-wg',0,0,1); INSERT INTO nodes VALUES (7,'native-node','127.0.0.1',0,0,'connected','','','certificate','key','custom',?,1); INSERT INTO users VALUES (7,'client-user',10,'active','0123456789abcdef0123456789abcdef',0,0,0,1,0,0,0); INSERT INTO proxies VALUES (7,'wireguard'); INSERT INTO hosts VALUES (1,'native-wg',0); INSERT INTO service_hosts VALUES (10,1); INSERT INTO wireguard_devices VALUES ('native-wg',7,0,'private',?,'10.200.0.2',1)`, string(raw), string(raw), primaryKey)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node-data"), 0700); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	return db, repo, NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: raw}, `{"inbounds":[]}`, wireGuardNativeConfigJSON(serverPrivate, iface, primaryKey)
}

func TestWireGuardNativeControllerFixtureBuildsRuntimePolicy(t *testing.T) {
	db, repo, _, _, native := wireGuardNativeControllerDB(t, context.Background(), t.TempDir(), "wg-native", strings.Repeat("A", 43)+"=", strings.Repeat("B", 43)+"=")
	defer db.Close()
	runtimeConfig, err := repo.WGRuntime(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeConfig.Inbounds) != 1 || len(runtimeConfig.Inbounds[0].Peers) != 1 || runtimeConfig.Inbounds[0].Peers[0].UserID != 7 {
		t.Fatalf("native fixture runtime=%+v", runtimeConfig)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(native), &decoded); err != nil || len(decoded) == 0 {
		t.Fatalf("initial native runtime JSON invalid: %v", err)
	}
}

func wireGuardNativeConfigJSON(serverPrivate, iface, primaryKey string) string {
	raw, _ := json.Marshal(map[string]any{"wg_inbounds": []any{map[string]any{
		"tag": "native-wg", "listen_port": 51820, "settings": map[string]any{"interface_name": iface, "private_key": serverPrivate, "address_pool": "10.200.0.0/24", "server_address": "10.200.0.1", "tproxy_enabled": false, "nat_enabled": false, "accounting_enabled": false},
		"peers": []any{map[string]any{"user_id": 7, "public_key": primaryKey, "address": "10.200.0.2", "status": "active"}},
	}}})
	return string(raw)
}

func envRequired(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("required native E2E environment variable %s is empty", key)
	}
	return value
}

func assertNativeWireGuardClient(t *testing.T, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ip", "netns", "exec", namespace, "ping", "-c", "1", "-W", "2", "10.200.0.1").CombinedOutput()
	if err != nil {
		t.Fatalf("unrelated native WireGuard client traffic failed: %v: %s", err, output)
	}
}

func nativeWireGuardHandshake(t *testing.T, iface, publicKey string) int64 {
	t.Helper()
	output, err := exec.Command("wg", "show", iface, "latest-handshakes").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == publicKey {
			var value int64
			if _, err := fmt.Sscan(fields[1], &value); err == nil {
				return value
			}
		}
	}
	t.Fatalf("native handshake for unrelated peer %s missing: %s", publicKey, output)
	return 0
}

func nativeWireGuardPeerPresent(t *testing.T, iface, address string) bool {
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
