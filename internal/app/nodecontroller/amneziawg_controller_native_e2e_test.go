//go:build linux

package nodecontroller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// The privileged AWG driver invokes this with the actual, already-established
// tunnel and NodeAgent state created by the production lifecycle harness.
func TestControllerAmneziaWGTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_AWG_CONTROLLER_TARGETED_MUTATION_E2E") != "1" {
		t.Skip("requires the privileged native AmneziaWG driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native AmneziaWG Controller E2E requires Linux root")
	}
	stateDir := envRequired(t, "ANTIMAGE_AWG_NATIVE_STATE")
	iface := envRequired(t, "ANTIMAGE_AWG_NATIVE_INTERFACE")
	clientNS := envRequired(t, "ANTIMAGE_AWG_NATIVE_CLIENT_NS")
	primaryKey := envRequired(t, "ANTIMAGE_AWG_CLIENT_PUBLIC")
	additionalKey := envRequired(t, "ANTIMAGE_AWG_EXTRA_CLIENT_PUBLIC")
	tool := envRequired(t, "ANTIMAGE_AWG_TOOL")
	if output, err := exec.Command(tool, "show", iface, "peers").CombinedOutput(); err != nil || !strings.Contains(string(output), primaryKey) || !strings.Contains(string(output), additionalKey) {
		t.Fatalf("native AWG interface is missing an existing peer: peers=%s err=%v", output, err)
	}
	before, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	initialHandshake := nativeAmneziaWGControllerHandshake(t, tool, iface, primaryKey)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Setenv("ANTIMAGE_PUBLIC_URL", "https://panel.example.test")
	root := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", root)
	db := nativeAmneziaWGControllerDB(t, ctx, root)
	defer db.Close()
	serverTLS, clientTLS := openVPNNativeControllerTLS(t)
	agent := nodeagent.New(nodeagent.Config{DataDir: stateDir})
	hello, err := agent.Hello(ctx, &nodev1.HelloRequest{MasterId: "native-awg-master"})
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{"targeted_user_add_remove_amneziawg", "targeted_user_update_amneziawg"} {
		if openVPNNativeControllerHasCapability(hello.GetRuntime().GetCapabilities(), capability) {
			t.Fatalf("AmneziaWG capability %s was advertised before the Controller-native gate", capability)
		}
	}
	capabilityJSON, err := json.Marshal(hello.GetRuntime().GetCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET node_capabilities=? WHERE id=7`, string(capabilityJSON)); err != nil {
		t.Fatal(err)
	}
	var syncCalls, addCalls atomic.Int32
	var dropAck atomic.Bool
	interceptor := func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncCalls.Add(1)
		}
		if _, ok := request.(*nodev1.InboundUserRequest); ok && info.FullMethod == nodev1.NodeRuntimeService_AddUser_FullMethodName {
			addCalls.Add(1)
		}
		response, callErr := handler(ctx, request)
		if callErr == nil && dropAck.Load() {
			if mutation, ok := request.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == "add_user-401-native-awg" && dropAck.CompareAndSwap(true, false) {
				return nil, status.Error(codes.Unavailable, "simulated AmneziaWG ACK loss after kernel and policy commit")
			}
		}
		return response, callErr
	}
	listener, grpcServer, address := startOpenVPNNativeControllerAgentWithInterceptor(t, agent, serverTLS, interceptor)
	defer func() { grpcServer.Stop(); _ = listener.Close() }()
	client, err := nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	node := NodeRow{ID: 7, Name: "native-awg-node", Address: "127.0.0.1", Status: "connected", Certificate: "native-cert", XrayConfigMode: "custom", XrayConfig: nativeAmneziaWGNodeConfig()}
	controller := NewController(NewRepository(db, "sqlite"))
	// The preceding native NodeAgent lifecycle/mutation E2E established and
	// persisted this same baseline policy against the live tunnel. Do not issue a
	// full SyncConfig here: that would deliberately re-apply ReplacePeers to a
	// healthy interface before measuring targeted Controller mutations.
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.2/32", true) || !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.3/32", true) {
		t.Fatal("native NodeAgent E2E did not leave the expected baseline peers")
	}
	nativeAmneziaWGControllerPing(t, clientNS, "before Controller mutation")
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.2/32", true) || !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.3/32", true) || nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.9/32", false) {
		t.Fatal("native baseline did not preserve the two live peers and leave the target absent")
	}
	// Until the native gate is complete the production Hello deliberately does
	// not advertise this capability. Seed only the Controller's handshake view
	// here to exercise the exact production targeted dispatch over real gRPC.
	capabilities := append(append([]string(nil), hello.GetRuntime().GetCapabilities()...), "targeted_user_add_remove_amneziawg", "targeted_user_update_amneziawg")
	client.SetHandshake(hello.GetNodeVersion(), capabilities)
	dropAck.Store(true)
	add := OperationRow{ID: 401, OperationType: "add_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	firstAddErr := controller.grpcApplyUserOperation(ctx, client, node, add, nil)
	if firstAddErr == nil {
		t.Fatal("lost ACK was reported as successful")
	}
	if dropAck.Load() {
		t.Fatalf("AddUser failed before the post-commit ACK-loss hook: %v", firstAddErr)
	}
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.9/32", true) {
		t.Fatal("AddUser did not immediately add target peer to the live AWG interface")
	}
	if err := controller.grpcApplyUserOperation(ctx, client, node, add, nil); err != nil {
		t.Fatalf("same Operation ID retry after lost ACK: %v", err)
	}
	nativeAmneziaWGControllerPing(t, clientNS, "after AddUser and ACK-loss retry")
	if addCalls.Load() != 2 {
		t.Fatalf("AddUser RPC count=%d, want initial attempt and one safe retry", addCalls.Load())
	}
	assertNativeAmneziaWGControllerPolicy(t, stateDir, 8, true)

	if _, err := db.ExecContext(ctx, `UPDATE amneziawg_devices SET address='10.74.0.10' WHERE inbound_tag='native-awg' AND user_id=8 AND device_index=0`); err != nil {
		t.Fatal(err)
	}
	update := OperationRow{ID: 402, OperationType: "update_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	if err := controller.grpcApplyUserOperation(ctx, client, node, update, nil); err != nil {
		t.Fatalf("Controller UpdateUser: %v", err)
	}
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.10/32", true) || nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.9/32", false) {
		t.Fatal("UpdateUser did not replace the target AllowedIP immediately")
	}
	nativeAmneziaWGControllerPing(t, clientNS, "after UpdateUser")

	// Reusing the successful Add operation ID with changed desired content must
	// be rejected and leave the committed target configuration untouched.
	if _, err := db.ExecContext(ctx, `UPDATE amneziawg_devices SET address='10.74.0.11' WHERE inbound_tag='native-awg' AND user_id=8 AND device_index=0`); err != nil {
		t.Fatal(err)
	}
	if err := controller.grpcApplyUserOperation(ctx, client, node, add, nil); err == nil {
		t.Fatal("conflicting content under a committed Operation ID was accepted")
	}
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.10/32", true) || nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.11/32", false) {
		t.Fatal("conflicting Operation ID changed the live target peer")
	}
	if _, err := db.ExecContext(ctx, `UPDATE amneziawg_devices SET address='10.74.0.10' WHERE inbound_tag='native-awg' AND user_id=8 AND device_index=0`); err != nil {
		t.Fatal(err)
	}

	remove := OperationRow{ID: 403, OperationType: "remove_user", UserID: sql.NullInt64{Int64: 8, Valid: true}}
	if err := controller.grpcApplyUserOperation(ctx, client, node, remove, nil); err != nil {
		t.Fatalf("Controller RemoveUser: %v", err)
	}
	if nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.10/32", false) {
		t.Fatal("RemoveUser left target peer in the live AWG interface")
	}
	assertNativeAmneziaWGControllerPolicy(t, stateDir, 8, false)

	if syncCalls.Load() != 0 {
		t.Fatalf("targeted Controller path invoked SyncConfig %d times", syncCalls.Load())
	}
	if !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.2/32", true) || !nativeAmneziaWGControllerPeer(t, tool, iface, "10.74.0.3/32", true) {
		t.Fatal("targeted user mutations disturbed existing AWG peers")
	}
	nativeAmneziaWGControllerPing(t, clientNS, "after RemoveUser")
	if got, err := net.InterfaceByName(iface); err != nil || got.Index != before.Index {
		t.Fatalf("targeted mutation recreated AWG interface: before=%d after=%v err=%v", before.Index, got, err)
	}
	if got := nativeAmneziaWGControllerHandshake(t, tool, iface, primaryKey); got < initialHandshake {
		t.Fatalf("unrelated peer handshake regressed: before=%d after=%d", initialHandshake, got)
	}
	t.Log("real Controller -> TLS/gRPC NodeAgent -> AmneziaWG kernel Add/Update/Remove passed; ACK-loss retry and conflicting Operation ID checks passed; policy ACK state, client traffic, unrelated peers, interface identity, and no-SyncConfig path verified")
}

func nativeAmneziaWGControllerDB(t *testing.T, ctx context.Context, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "awg-controller-native.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl := `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, service_id INTEGER, status TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 1, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER DEFAULT 0, downlink INTEGER DEFAULT 0, usage_coefficient REAL DEFAULT 1);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT, address TEXT, port INTEGER, api_port INTEGER, status TEXT, xray_version TEXT, message TEXT, certificate TEXT, certificate_key TEXT, xray_config_mode TEXT, xray_config TEXT, usage_coefficient REAL DEFAULT 1, node_capabilities TEXT);
CREATE TABLE amneziawg_devices (inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL, private_key TEXT NOT NULL, public_key TEXT NOT NULL, preshared_key TEXT NOT NULL DEFAULT '', address TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(inbound_tag,user_id,device_index), UNIQUE(inbound_tag,address));
INSERT INTO users VALUES (7,'native-awg',10,'active',0,0,0,2,0,0,0);
INSERT INTO proxies VALUES (7,'amneziawg');
INSERT INTO users VALUES (8,'native-target',10,'active',0,0,0,1,0,0,0);
INSERT INTO proxies VALUES (8,'amneziawg');
INSERT INTO hosts VALUES (1,'native-awg',0);
INSERT INTO service_hosts VALUES (10,1);
INSERT INTO inbounds VALUES ('native-awg',0,0,1);
INSERT INTO xray_config VALUES (1,'{"inbounds":[{"tag":"native-awg","protocol":"amneziawg","port":51821,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.74.0.0/24","server_address":"10.74.0.1/24","accounting_enabled":false}}]}');
INSERT INTO nodes VALUES (7,'native-awg-node','127.0.0.1',0,0,'connected','','','native-cert','key','custom','{"inbounds":[{"tag":"native-awg","protocol":"amneziawg","port":51821,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.74.0.0/24","server_address":"10.74.0.1/24","accounting_enabled":false}}]}',1,NULL);`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`,
		`CREATE TABLE operation_locks (target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
		`CREATE TABLE operation_events (operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
		`CREATE TABLE operation_executor_leases (operation_id TEXT PRIMARY KEY,executor_id TEXT NOT NULL,acquired_at BIGINT NOT NULL,expires_at BIGINT NOT NULL,generation BIGINT NOT NULL,revision BIGINT NOT NULL)`,
		`CREATE TABLE operation_resource_fences (target_type TEXT NOT NULL,target_id TEXT NOT NULL,generation BIGINT NOT NULL,owner_operation_id TEXT NOT NULL,revision BIGINT NOT NULL,PRIMARY KEY(target_type,target_id))`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO amneziawg_devices VALUES ('native-awg',7,0,'existing-private-1',?,'','10.74.0.2',1), ('native-awg',7,1,'existing-private-2',?,'','10.74.0.3',1)`, envRequired(t, "ANTIMAGE_AWG_CLIENT_PUBLIC"), envRequired(t, "ANTIMAGE_AWG_EXTRA_CLIENT_PUBLIC")); err != nil {
		t.Fatal(err)
	}
	private, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		t.Fatal(err)
	}
	publicCmd := exec.Command("wg", "pubkey")
	publicCmd.Stdin = strings.NewReader(strings.TrimSpace(string(private)) + "\n")
	public, err := publicCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO amneziawg_devices VALUES ('native-awg',8,0,?,?, '', '10.74.0.9',1)`, strings.TrimSpace(string(private)), strings.TrimSpace(string(public))); err != nil {
		t.Fatal(err)
	}
	return db
}

func nativeAmneziaWGNodeConfig() []byte {
	raw, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"tag": "native-awg", "protocol": "amneziawg", "port": 51821, "targets": []any{"node:7"}, "settings": map[string]any{"ipv4_pool_cidr": "10.74.0.0/24", "server_address": "10.74.0.1/24", "accounting_enabled": false}}}})
	return raw
}

func nativeAmneziaWGControllerRuntimeJSON(t *testing.T, primaryKey, additionalKey string) string {
	t.Helper()
	callbackURL := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SESSION_CALLBACK_URL"))
	callbackToken := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SESSION_CALLBACK_TOKEN"))
	if callbackURL != "" && callbackToken == "" {
		t.Fatal("native AWG session callback token is required when callback URL is configured")
	}
	var dataLimit *int64
	if rawLimit := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_QUOTA_BYTES")); rawLimit != "" {
		parsed, err := strconv.ParseInt(rawLimit, 10, 64)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid native AWG quota %q", rawLimit)
		}
		dataLimit = &parsed
	}
	primaryPeer := map[string]any{"user_id": 7, "username": "native-awg", "device_index": 1, "public_key": primaryKey, "address": "10.74.0.2", "status": "active", "usage_coefficient": 1.5, "inbound_coefficient": 2}
	extraPeer := map[string]any{"user_id": 7, "username": "native-awg-extra", "device_index": 2, "public_key": additionalKey, "address": "10.74.0.3", "status": "active", "usage_coefficient": 1.5, "inbound_coefficient": 2}
	if dataLimit != nil {
		primaryPeer["data_limit"] = dataLimit
		extraPeer["data_limit"] = dataLimit
	}
	payload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"target":       "node:7",
		"session_callback": map[string]any{
			"url": callbackURL, "token": callbackToken, "node_id": 7,
		},
		"inbounds":      []any{},
		"l2tp_inbounds": []any{},
		"pptp_inbounds": []any{},
		"wg_inbounds":   []any{},
		"awg_inbounds": []any{map[string]any{
			"tag": "native-awg", "listen_port": 51821,
			"settings": map[string]any{
				"private_key":  strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SERVER_PRIVATE")),
				"address_pool": "10.74.0.0/24", "server_address": "10.74.0.1/24", "mtu": 1420,
				"jc": 4, "jmin": 8, "jmax": 80, "s1": 77, "s2": 90,
				"h1": "12345", "h2": "23456", "h3": "34567", "h4": "45678",
			},
			"peers": []any{
				primaryPeer,
				extraPeer,
			},
		}},
		"ikev2_inbounds":      []any{},
		"anyconnect_inbounds": []any{},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func nativeAmneziaWGControllerPeer(t *testing.T, tool, iface, allowedIP string, want bool) bool {
	t.Helper()
	out, err := exec.Command(tool, "show", iface, "allowed-ips").CombinedOutput()
	if err != nil {
		t.Fatalf("read live AmneziaWG peer state: %v: %s", err, out)
	}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if len(strings.Fields(line)) == 2 && strings.Contains(strings.Fields(line)[1], allowedIP) {
			found = true
		}
	}
	if found != want {
		t.Fatalf("live AmneziaWG AllowedIP %s present=%v want=%v: %s", allowedIP, found, want, out)
	}
	return found
}

func nativeAmneziaWGControllerHandshake(t *testing.T, tool, iface, key string) int64 {
	t.Helper()
	out, err := exec.Command(tool, "show", iface, "latest-handshakes").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			var value int64
			if _, err := fmt.Sscan(fields[1], &value); err == nil {
				return value
			}
		}
	}
	t.Fatalf("native AWG peer handshake for %s missing: %s", key, out)
	return 0
}

func nativeAmneziaWGControllerPing(t *testing.T, namespace, stage string) {
	t.Helper()
	// Use an echoed TCP payload through the live tunnel instead of a single ICMP
	// packet. This proves both directions of application traffic after mutation
	// and avoids treating a transient echo loss as evidence of a broken peer.
	listener, err := net.Listen("tcp", "10.74.0.1:0")
	if err != nil {
		t.Fatalf("listen on native AWG server address at %s: %v", stage, err)
	}
	defer listener.Close()
	payload := bytes.Repeat([]byte("antimage-awg-unrelated-peer-traffic\n"), 128)
	type result struct {
		data []byte
		err  error
	}
	serverResult := make(chan result, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- result{err: acceptErr}
			return
		}
		defer conn.Close()
		data, readErr := io.ReadAll(conn)
		if readErr == nil {
			_, readErr = conn.Write(data)
		}
		serverResult <- result{data: data, err: readErr}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	port := listener.Addr().(*net.TCPAddr).Port
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", namespace, "nc", "-N", "-w", "5", "10.74.0.1", strconv.Itoa(port))
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("unrelated native AWG TCP tunnel traffic failed %s: %v: %s", stage, err, out)
	}
	select {
	case got := <-serverResult:
		if got.err != nil || !bytes.Equal(got.data, payload) || !bytes.Equal(out, payload) {
			t.Fatalf("unrelated native AWG TCP tunnel traffic mismatch %s: server_bytes=%d client_bytes=%d want=%d err=%v", stage, len(got.data), len(out), len(payload), got.err)
		}
	case <-ctx.Done():
		t.Fatalf("unrelated native AWG TCP tunnel traffic timed out %s: %v", stage, ctx.Err())
	}
}

func assertNativeAmneziaWGControllerPolicy(t *testing.T, stateDir string, userID int64, wantPresent bool) {
	t.Helper()
	path := filepath.Join(stateDir, "accounting", "runtime-policy.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		NativeJSON string `json:"native_json"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		AWG []struct {
			Peers []struct {
				UserID int64 `json:"user_id"`
			} `json:"peers"`
		} `json:"awg_inbounds"`
	}
	if err := json.Unmarshal([]byte(persisted.NativeJSON), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, inbound := range payload.AWG {
		for _, peer := range inbound.Peers {
			found = found || peer.UserID == userID
		}
	}
	if found != wantPresent {
		t.Fatalf("persisted ACK policy user %d present=%v want=%v", userID, found, wantPresent)
	}
}
