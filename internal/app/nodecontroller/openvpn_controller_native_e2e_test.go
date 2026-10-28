package nodecontroller

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
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
	operationapp "github.com/antimage/antimage/internal/app/operations"
	userapp "github.com/antimage/antimage/internal/app/user"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

const nativeOpenVPNTestCapabilities = "[\"targeted_user_add_remove_openvpn\",\"targeted_user_update_openvpn\"]"
const nativeOpenVPNTestNodeCapabilities = "[\"shared_fencing_v1\",\"command_idempotency_v1\",\"config_revision\",\"targeted_user_add_remove_openvpn\",\"targeted_user_update_openvpn\"]"

// This privileged integration test exercises the production NodeAgent
// capabilities over gRPC and never injects them through a test-only shim.
func TestControllerOpenVPNTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_OPENVPN_MUTATION_E2E") != "1" {
		t.Skip("requires the authorized privileged OpenVPN native E2E driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native Controller OpenVPN E2E requires Linux root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("native Controller OpenVPN E2E requires /dev/net/tun: %v", err)
	}
	root := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_CONTROLLER_ROOT"))
	ns := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_NS"))
	caPath := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_CA"))
	serverCertPath := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_CERT"))
	serverKeyPath := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_KEY"))
	openVPNPort, err := strconv.Atoi(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_PORT"))
	if root == "" || ns == "" || caPath == "" || serverCertPath == "" || serverKeyPath == "" || err != nil || openVPNPort <= 0 {
		t.Fatal("native Controller OpenVPN E2E requires the privileged driver's namespace, root, certificate, key, and port")
	}
	if output, err := exec.Command("ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		t.Fatalf("authorized network namespace %q unavailable: %v: %s", ns, err, output)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_NODE_APP_DIR", root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	serverTLS, clientTLS := openVPNNativeControllerTLS(t)
	db, repo, node, runtimeConfig, initialNative := openVPNNativeControllerDB(t, ctx, root, caPath, serverCertPath, serverKeyPath, serverTLS.Certificates[0])
	defer db.Close()
	dataDir := filepath.Join(root, "node-agent")
	agent := nodeagent.New(nodeagent.Config{DataDir: dataDir})
	hello, err := agent.Hello(ctx, &nodev1.HelloRequest{MasterId: "antimage-master"})
	if err != nil {
		t.Fatalf("read production NodeAgent capabilities: %v", err)
	}
	for _, capability := range []string{"targeted_user_add_remove_openvpn", "targeted_user_update_openvpn"} {
		if !openVPNNativeControllerHasCapability(hello.GetRuntime().GetCapabilities(), capability) {
			t.Fatalf("production NodeAgent did not advertise gated OpenVPN capability %q", capability)
		}
	}
	listener, grpcServer, address := startOpenVPNNativeControllerAgent(t, agent, serverTLS, true)
	defer func() {
		grpcServer.Stop()
		_ = listener.Close()
		_, _ = agent.StopRuntime(context.Background(), &nodev1.StopRuntimeRequest{})
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET port = ? WHERE id = 7`, port); err != nil {
		t.Fatal(err)
	}
	node.Address, node.Port = "127.0.0.1", port
	controller := NewController(repo)
	client, err := nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial native NodeAgent: %v", err)
	}
	defer client.Close()
	initialRequest := &nodev1.RuntimeConfigRequest{
		OperationId: "native-openvpn-initial-100", ConfigJson: runtimeConfig,
		OvRuntimeJson: initialNative, DesiredRevision: 100,
	}
	if err := controller.executeLegacyNodeCommand(legacyConfigEvidence(ctx, runtimeConfig), 7, initialRequest.GetOperationId(), "sync_config", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		initialRequest.Fence = fence
		return client.Runtime().SyncConfig(worker, initialRequest)
	}, nil); err != nil {
		t.Fatalf("Controller initial native OpenVPN sync: %v", err)
	}
	serverConfig := filepath.Join(dataDir, "openvpn", openVPNNativeDirName("ov-in"), "server.conf")
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerPID(serverConfig) > 0 }); err != nil {
		t.Fatalf("wait for OpenVPN daemon: %v", err)
	}
	tunName := openVPNNativeControllerTunName("ov-in")
	if err := waitOpenVPNNativeController(func() bool { _, e := net.InterfaceByName(tunName); return e == nil }); err != nil {
		t.Fatalf("wait for server TUN %s: %v", tunName, err)
	}
	pidBefore := openVPNNativeControllerPID(serverConfig)
	tunBefore, err := net.InterfaceByName(tunName)
	if err != nil {
		t.Fatal(err)
	}
	alicePassword, err := userapp.OVPasswordFromCredentialKey("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	alice := startOpenVPNNativeControllerClient(t, root, ns, caPath, openVPNPort, "alice", alicePassword)
	defer alice.stop()
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerHasClient(dataDir, "alice") }); err != nil {
		t.Fatalf("Alice did not establish a real OpenVPN session: %v", err)
	}
	assertOpenVPNNativeControllerTraffic(t, ns)
	aliceTun := openVPNNativeControllerClientTunIndex(t, ns, alice.dev)

	var syncCalls atomic.Int32
	var dropAckFor atomic.Value
	var failBeforeHandler atomic.Value
	dropAckFor.Store("")
	failBeforeHandler.Store("")
	var nativeInterceptor grpc.UnaryServerInterceptor
	// Start a fresh gRPC service with deterministic ACK-loss injection after
	// the real NodeAgent handler has committed its files, receipt, and policy.
	grpcServer.Stop()
	_ = listener.Close()
	nativeInterceptor = func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncCalls.Add(1)
		}
		if mutation, ok := req.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == failBeforeHandler.Load() {
			failBeforeHandler.Store("")
			return nil, status.Error(codes.Unavailable, "simulated connection loss before NodeAgent mutation")
		}
		response, callErr := handler(ctx, req)
		if mutation, ok := req.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == dropAckFor.Load() && callErr == nil {
			dropAckFor.Store("")
			return nil, status.Error(codes.Unavailable, "simulated connection loss after NodeAgent commit, before ACK")
		}
		return response, callErr
	}
	listener, grpcServer, address = startOpenVPNNativeControllerAgentWithInterceptor(t, agent, serverTLS, nativeInterceptor)
	port = listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET port = ? WHERE id = 7`, port); err != nil {
		t.Fatal(err)
	}
	node.Address, node.Port = "127.0.0.1", port
	client, err = nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial re-registered native NodeAgent: %v", err)
	}
	defer client.Close()
	controller = NewController(repo)

	// AddUser is committed by the real NodeAgent, but the controller loses the
	// first ACK. A new Controller instance replays the same durable operation ID.
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id,username,credential_key,service_id,status,used_traffic,data_limit,expire,device_limit,ip_limit,upload_speed_limit,download_speed_limit) VALUES (8,'bob','1123456789abcdef0123456789abcdef',10,'active',0,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO proxies (user_id,type) VALUES (8,'openvpn')`); err != nil {
		t.Fatal(err)
	}
	bobPassword, err := userapp.OVPasswordFromCredentialKey("1123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := queueOpenVPNNativeControllerOperation(ctx, db, 101, "add_user", 8); err != nil {
		t.Fatal(err)
	}
	dropAckFor.Store("add_user-101-ov-in")
	result, err := controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Retrying != 1 || result.Done != 0 {
		t.Fatalf("lost-ACK first delivery result=%+v err=%v", result, err)
	}
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerCredential(dataDir, "bob", bobPassword) }); err != nil {
		t.Fatalf("NodeAgent did not persist AddUser before lost ACK: %v", err)
	}
	credentialPath := filepath.Join(dataDir, "openvpn", openVPNNativeDirName("ov-in"), "credentials.sha256")
	credentialBeforeReplay, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE node_operations SET status='pending', updated_at=? WHERE id=101`, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	controller = NewController(repo) // controller restart over the durable queue
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 || result.Retrying != 0 {
		t.Fatalf("lost-ACK replay after Controller restart result=%+v err=%v", result, err)
	}
	credentialAfterReplay, err := os.Stat(credentialPath)
	if err != nil || !credentialAfterReplay.ModTime().Equal(credentialBeforeReplay.ModTime()) {
		t.Fatalf("lost-ACK replay rewrote already-committed OpenVPN credential state: before=%v after=%v err=%v", credentialBeforeReplay.ModTime(), credentialAfterReplay, err)
	}
	if got := openVPNNativeControllerPID(serverConfig); got != pidBefore {
		t.Fatalf("AddUser restarted OpenVPN daemon: PID before=%d after=%d", pidBefore, got)
	}
	if got, err := net.InterfaceByName(tunName); err != nil || got.Index != tunBefore.Index {
		t.Fatalf("AddUser changed server TUN identity: before=%d after=%v err=%v", tunBefore.Index, got, err)
	}
	bob := startOpenVPNNativeControllerClient(t, root, ns, caPath, openVPNPort, "bob", bobPassword)
	defer bob.stop()
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerHasClient(dataDir, "bob") }); err != nil {
		t.Fatalf("Bob did not establish after Controller AddUser: %v", err)
	}
	assertOpenVPNNativeControllerClients(t, dataDir, "alice", "bob")
	assertOpenVPNNativeControllerTraffic(t, ns)
	assertOpenVPNNativeControllerClientTun(t, ns, alice.dev, aliceTun)
	if got := openVPNNativeControllerReceipt(filepath.Join(dataDir, "accounting", "runtime-policy.json"), 101, "add_user-101-ov-in"); got == "" {
		t.Fatal("the NodeAgent did not retain the idempotency receipt for the ACK-lost AddUser")
	}
	if got := openVPNNativeControllerOperationStatus(t, db, 101); got != "done" {
		t.Fatalf("controller did not durably persist the replayed ACK: operation status=%q", got)
	}
	conflicting, err := grpcOpenVPNMutationUser(OVRuntimeUser{UserID: 9, Username: "mallory", VPNUsername: "mallory", Password: "conflicting", Status: "active"}, "add", 101)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Runtime().AddUser(ctx, &nodev1.InboundUserRequest{OperationId: "add_user-101-ov-in", InboundTag: "ov-in", User: conflicting}); err == nil {
		t.Fatal("reused OpenVPN operation ID with conflicting desired state was accepted")
	}

	// Update and remove use independent durable operation IDs and the same
	// Controller queue path; Alice's active connection and tunnel traffic stay live.
	if _, err := db.ExecContext(ctx, `UPDATE users SET credential_key='2123456789abcdef0123456789abcdef' WHERE id=8`); err != nil {
		t.Fatal(err)
	}
	if err := queueOpenVPNNativeControllerOperation(ctx, db, 102, "update_user", 8); err != nil {
		t.Fatal(err)
	}
	failBeforeHandler.Store("update_user-102-ov-in")
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Retrying != 1 || openVPNNativeControllerCredential(dataDir, "bob", bobPassword) == false {
		t.Fatalf("pre-handler RPC interruption result=%+v err=%v; prior credential should remain", result, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE node_operations SET status='pending', updated_at=? WHERE id=102`, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	controller = NewController(repo)
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 {
		t.Fatalf("Controller UpdateUser retry result=%+v err=%v", result, err)
	}
	bob.stop()
	bobPassword, err = userapp.OVPasswordFromCredentialKey("2123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	bob = startOpenVPNNativeControllerClient(t, root, ns, caPath, openVPNPort, "bob", bobPassword)
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerHasClient(dataDir, "bob") }); err != nil {
		t.Fatalf("updated OpenVPN credential was not accepted immediately: %v", err)
	}
	assertOpenVPNNativeControllerStable(t, serverConfig, pidBefore, tunName, tunBefore.Index, dataDir, ns, alice.dev, aliceTun)

	if _, err := db.ExecContext(ctx, `UPDATE users SET status='disabled' WHERE id=8`); err != nil {
		t.Fatal(err)
	}
	if err := queueOpenVPNNativeControllerOperation(ctx, db, 103, "disable_user", 8); err != nil {
		t.Fatal(err)
	}
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 {
		t.Fatalf("Controller RemoveUser result=%+v err=%v", result, err)
	}
	if err := waitOpenVPNNativeController(func() bool { return !openVPNNativeControllerHasClient(dataDir, "bob") }); err != nil {
		t.Fatalf("RemoveUser did not revoke Bob's live session: %v", err)
	}
	assertOpenVPNNativeControllerStable(t, serverConfig, pidBefore, tunName, tunBefore.Index, dataDir, ns, alice.dev, aliceTun)
	if openVPNNativeControllerCredential(dataDir, "bob", bobPassword) {
		t.Fatal("RemoveUser left Bob's credential in the live auth file")
	}
	if syncCalls.Load() != 0 {
		t.Fatalf("targeted OpenVPN queue invoked SyncConfig %d times after initial setup", syncCalls.Load())
	}

	// Simulate a real NodeAgent process restart. The old agent stops its owned
	// daemon; a fresh agent loads durable policy from the same data directory,
	// then Controller reconciliation applies current DB desired state again.
	if err := controller.StopNodeRuntime(ctx, 7); err != nil {
		t.Fatalf("Controller could not verify OpenVPN native daemon stop before NodeAgent restart: %v", err)
	}
	if err := waitOpenVPNNativeController(func() bool { return openVPNNativeControllerPID(serverConfig) == 0 }); err != nil {
		t.Fatalf("OpenVPN daemon did not stop before NodeAgent restart: %v", err)
	}
	_ = client.Close()
	grpcServer.Stop()
	_ = listener.Close()
	agent = nodeagent.New(nodeagent.Config{DataDir: dataDir})
	listener, grpcServer, address = startOpenVPNNativeControllerAgentWithInterceptor(t, agent, serverTLS, nativeInterceptor)
	port = listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET port = ? WHERE id = 7`, port); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET desired_revision=103 WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	node.Address, node.Port = "127.0.0.1", port
	client, err = nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial restarted NodeAgent: %v", err)
	}
	controller = NewController(repo)
	prepared := &preparedRuntimeConfig{nodeID: 7, configJSON: runtimeConfig, ovRuntimeJSON: initialNative}
	if err := controller.applyOperation(ctx, OperationRow{
		ID: 104, OperationType: "sync_config", NodeID: sql.NullInt64{Int64: 7, Valid: true},
		Payload: json.RawMessage(`{"config_json":"{\"inbounds\":[]}"}`),
	}, prepared); err != nil {
		t.Fatalf("Controller reconciliation after NodeAgent restart: %v", err)
	}
	if err := waitOpenVPNNativeControllerFor(25*time.Second, func() bool {
		return openVPNNativeControllerPID(serverConfig) > 0 && openVPNNativeControllerHasClient(dataDir, "alice")
	}); err != nil {
		t.Fatalf("Alice did not reconnect after NodeAgent recovery sync: %v", err)
	}
	assertOpenVPNNativeControllerTraffic(t, ns)
	assertOpenVPNNativeControllerClients(t, dataDir, "alice")
	if syncCalls.Load() != 1 {
		t.Fatalf("expected exactly one explicit recovery SyncConfig after restart; got %d", syncCalls.Load())
	}
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Processed != 0 || result.Done != 0 {
		t.Fatalf("completed operations replayed after Controller/NodeAgent restart: result=%+v err=%v", result, err)
	}
	_ = client.Close()
}

func TestOpenVPNNativeControllerFixtureAllowsGatedTargetedPreparation(t *testing.T) {
	t.Setenv("ANTIMAGE_OPENVPN_MUTATION_PORT", "11942")
	root := t.TempDir()
	ca, cert, key := filepath.Join(root, "ca.crt"), filepath.Join(root, "server.crt"), filepath.Join(root, "server.key")
	for _, path := range []string{ca, cert, key} {
		if err := os.WriteFile(path, []byte("native fixture material"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	serverTLS, _ := openVPNNativeControllerTLS(t)
	db, repo, node, _, _ := openVPNNativeControllerDB(t, ctx, root, ca, cert, key, serverTLS.Certificates[0])
	defer db.Close()
	controller := NewController(repo)
	prepared, err := controller.prepareRuntimeConfigOperation(ctx, OperationRow{
		ID: 101, OperationType: "update_user", NodeID: sql.NullInt64{Int64: 7, Valid: true},
		UserID: sql.NullInt64{Int64: 7, Valid: true},
	})
	if err != nil {
		t.Fatalf("prepare native OpenVPN targeted mutation for node %d: %v", node.ID, err)
	}
	if required, decided := prepared.userSyncDecision(node.ID); !decided || required {
		t.Fatalf("OpenVPN targeted preparation decided required=%v decided=%v", required, decided)
	}
}

func openVPNNativeControllerDB(t *testing.T, ctx context.Context, root, caPath, certPath, keyPath string, identity tls.Certificate) (*sql.DB, Repository, NodeRow, string, string) {
	t.Helper()
	cert, key := openVPNNativeControllerTLSPEM(t, identity)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "controller-native.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TABLE nodes (id INTEGER PRIMARY KEY,name TEXT,address TEXT,port INTEGER,api_port INTEGER,status TEXT,xray_version TEXT,message TEXT,certificate TEXT,certificate_key TEXT,xray_config_mode TEXT,xray_config TEXT,last_status_change DATETIME,usage_coefficient REAL DEFAULT 1,node_capabilities TEXT DEFAULT '[]',desired_revision INTEGER DEFAULT 0,applied_revision INTEGER DEFAULT 0,agent_status TEXT,xray_status TEXT,last_seen_at DATETIME);
CREATE TABLE tls (id INTEGER PRIMARY KEY,certificate TEXT,"key" TEXT);
CREATE TABLE node_operations (id INTEGER PRIMARY KEY AUTOINCREMENT,operation_type TEXT NOT NULL,node_id INTEGER,user_id INTEGER,payload TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT,idempotency_key TEXT NOT NULL UNIQUE,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL);
CREATE TABLE users (id INTEGER PRIMARY KEY,username TEXT,credential_key TEXT,service_id INTEGER,status TEXT,used_traffic INTEGER DEFAULT 0,data_limit INTEGER,expire INTEGER,device_limit INTEGER DEFAULT 0,ip_limit INTEGER DEFAULT 0,upload_speed_limit INTEGER DEFAULT 0,download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER,type TEXT);
CREATE TABLE hosts (id INTEGER PRIMARY KEY,inbound_tag TEXT,is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE service_hosts (service_id INTEGER,host_id INTEGER);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY,data TEXT,created_at DATETIME,updated_at DATETIME);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY,uplink INTEGER DEFAULT 0,downlink INTEGER DEFAULT 0,usage_coefficient REAL DEFAULT 1,upload_speed_limit INTEGER NOT NULL DEFAULT 0,download_speed_limit INTEGER NOT NULL DEFAULT 0);
CREATE TABLE wireguard_peer_addresses (inbound_tag TEXT,user_id INTEGER,pool TEXT,server_address TEXT,address TEXT,PRIMARY KEY(inbound_tag,user_id),UNIQUE(inbound_tag,address));
CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT);
CREATE TABLE operation_locks (target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id));
CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence));
`+operationapp.ExecutorLeaseDDL+`;
`+operationapp.ResourceFenceDDL+`;
`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	config := nativeOpenVPNControllerNodeConfig(caPath, certPath, keyPath)
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tls VALUES (1,?,?)`, []any{string(cert), string(key)}},
		{`INSERT INTO xray_config VALUES (1,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{config}},
		{`INSERT INTO nodes VALUES (7,'native-node','127.0.0.1',0,0,'connected','','',?,?,'custom',?,CURRENT_TIMESTAMP,1,?,100,100,'connected','running',CURRENT_TIMESTAMP)`, []any{string(cert), string(key), config, string(nativeOpenVPNTestNodeCapabilities)}},
		{`INSERT INTO users VALUES (7,'alice','0123456789abcdef0123456789abcdef',10,'active',0,0,0,0,0,0,0)`, nil},
		{`INSERT INTO proxies VALUES (7,'openvpn')`, nil},
		{`INSERT INTO hosts VALUES (1,'ov-in',0)`, nil},
		{`INSERT INTO service_hosts VALUES (10,1)`, nil},
		{`INSERT INTO inbounds (tag,uplink,downlink,usage_coefficient) VALUES ('ov-in',0,0,1)`, nil},
	} {
		if _, err := db.ExecContext(ctx, seed.query, seed.args...); err != nil {
			_ = db.Close()
			t.Fatalf("seed OpenVPN Controller E2E database: %v", err)
		}
	}
	var storedCapabilities string
	if err := db.QueryRowContext(ctx, `SELECT node_capabilities FROM nodes WHERE id=7`).Scan(&storedCapabilities); err != nil || storedCapabilities != nativeOpenVPNTestNodeCapabilities || !json.Valid([]byte(storedCapabilities)) {
		_ = db.Close()
		t.Fatalf("seeded node capabilities=%q err=%v", storedCapabilities, err)
	}
	repo := NewRepository(db, "sqlite")
	node, err := repo.Node(ctx, 7)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	raw, err := repo.OVUsersForServices(ctx, "ov-in", []int64{10}, "10.211.0.0/24")
	if err != nil || len(raw) != 1 {
		_ = db.Close()
		t.Fatalf("load native OpenVPN user: users=%+v err=%v", raw, err)
	}
	settings := map[string]any{"ca": readNativeOpenVPNText(t, caPath), "server_certificate": readNativeOpenVPNText(t, certPath), "server_key": readNativeOpenVPNText(t, keyPath), "ipv4_pool_cidr": "10.211.0.0/24", "redirect_gateway": false, "accounting_enabled": false, "tproxy_enabled": false}
	native, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"tag": "ov-in", "tunnel_tag": "__antimage_ov_tunnel__ov-in", "port": openVPNNativeControllerPort(t), "transport": "udp", "settings": settings, "users": raw}}})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, repo, node, `{"inbounds":[]}`, string(native)
}

func nativeOpenVPNControllerNodeConfig(ca, cert, key string) string {
	raw, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"tag": "ov-in", "protocol": "openvpn", "port": openVPNNativeControllerPortValue(), "targets": []any{"node:7"}, "settings": map[string]any{"ipv4_pool_cidr": "10.211.0.0/24", "ca": ca, "server_certificate": cert, "server_key": key, "redirect_gateway": false, "accounting_enabled": false, "tproxy_enabled": false}}}})
	return string(raw)
}

func openVPNNativeControllerTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	fixture := httptest.NewTLSServer(nil)
	defer fixture.Close()
	certificate := fixture.TLS.Certificates[0]
	root, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	server := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	client := &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	return server, client
}

func openVPNNativeControllerTLSPEM(t *testing.T, certificate tls.Certificate) ([]byte, []byte) {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	privateDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	return certPEM, keyPEM
}

func startOpenVPNNativeControllerAgent(t *testing.T, agent *nodeagent.Server, tlsConfig *tls.Config, _ bool) (net.Listener, *grpc.Server, net.Addr) {
	return startOpenVPNNativeControllerAgentWithInterceptor(t, agent, tlsConfig, nil)
}

func startOpenVPNNativeControllerAgentWithInterceptor(t *testing.T, agent *nodeagent.Server, tlsConfig *tls.Config, interceptor grpc.UnaryServerInterceptor) (net.Listener, *grpc.Server, net.Addr) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	options := []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsConfig.Clone()))}
	if interceptor != nil {
		options = append(options, grpc.UnaryInterceptor(interceptor))
	}
	server := grpc.NewServer(options...)
	nodev1.RegisterNodeControlServiceServer(server, agent)
	nodev1.RegisterNodeRuntimeServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	return listener, server, listener.Addr()
}

func openVPNNativeControllerHasCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func queueOpenVPNNativeControllerOperation(ctx context.Context, db *sql.DB, id int64, kind string, userID int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO node_operations(id,operation_type,node_id,user_id,payload,status,idempotency_key,created_at,updated_at) VALUES(?,?,7,?,'{}','pending',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id, kind, userID, fmt.Sprintf("native-%d", id))
	return err
}

func openVPNNativeControllerOperationStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT status FROM node_operations WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func readNativeOpenVPNText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func openVPNNativeControllerPort(t *testing.T) int {
	t.Helper()
	value, err := strconv.Atoi(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_PORT"))
	if err != nil || value <= 0 {
		t.Fatal("invalid OpenVPN port")
	}
	return value
}
func openVPNNativeControllerPortValue() int {
	value, _ := strconv.Atoi(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_PORT"))
	return value
}
func openVPNNativeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}
func openVPNNativeControllerTunName(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return fmt.Sprintf("amov%x", sum[:4])
}

type openVPNNativeControllerClient struct {
	cmd *exec.Cmd
	dev string
}

func startOpenVPNNativeControllerClient(t *testing.T, root, ns, ca string, port int, user, password string) *openVPNNativeControllerClient {
	t.Helper()
	name := fmt.Sprintf("%s-%d", user, time.Now().UnixNano())
	auth, config := filepath.Join(root, name+".auth"), filepath.Join(root, name+".conf")
	if err := os.WriteFile(auth, []byte(user+"\n"+password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dev := "t" + fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:10]
	// Bound keepalive and reconnection time in this recovery test. The server
	// emits keepalives every 10 seconds, so the restart deadline leaves margin.
	contents := fmt.Sprintf("client\nremote 10.250.0.1 %d\nproto udp\ndev %s\ndev-type tun\nnobind\nresolv-retry infinite\nconnect-retry 1 1\nconnect-retry-max 0\nping 2\nping-restart 14\npull-filter ignore \"ping\"\npull-filter ignore \"ping-restart\"\npersist-key\npersist-tun\nca %s\nremote-cert-tls server\nverb 3\nauth-user-pass %s\nauth-nocache\n", port, dev, ca, auth)
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, "openvpn", "--config", config)
	log, err := os.OpenFile(filepath.Join(root, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	client := &openVPNNativeControllerClient{cmd: cmd, dev: dev}
	t.Cleanup(client.stop)
	return client
}
func (c *openVPNNativeControllerClient) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
}

func openVPNNativeControllerCredential(dataDir, user, password string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, "openvpn", openVPNNativeDirName("ov-in"), "credentials.sha256"))
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(user + "\x00" + password))
	return strings.Contains(string(raw), fmt.Sprintf("%x\t", sum[:]))
}
func openVPNNativeControllerHasClient(dataDir, user string) bool {
	raw, err := openVPNNativeControllerStatus(dataDir)
	return err == nil && strings.Contains(strings.ToLower(raw), strings.ToLower(user))
}

func assertOpenVPNNativeControllerClients(t *testing.T, dataDir string, users ...string) {
	t.Helper()
	raw, err := openVPNNativeControllerStatus(dataDir)
	if err != nil {
		t.Fatalf("read native OpenVPN session status: %v", err)
	}
	for _, user := range users {
		if !strings.Contains(strings.ToLower(raw), strings.ToLower(user)) {
			t.Fatalf("OpenVPN session for %s is absent: %s", user, raw)
		}
	}
}
func openVPNNativeControllerClients(t *testing.T, dataDir string, users ...string) {
	t.Helper()
	raw, err := openVPNNativeControllerStatus(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if !strings.Contains(strings.ToLower(raw), strings.ToLower(user)) {
			t.Fatalf("OpenVPN status missing %s: %s", user, raw)
		}
	}
}
func openVPNNativeControllerStatus(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "openvpn", openVPNNativeDirName("ov-in"), "management.sock")
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("status 3\n"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := ioReadUntilEnd(conn)
	return raw, err
}
func ioReadUntilEnd(conn net.Conn) (string, error) {
	var b strings.Builder
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		_, _ = b.WriteString(line)
		if strings.TrimSpace(line) == "END" {
			return b.String(), nil
		}
		if err != nil {
			return b.String(), err
		}
	}
}
func waitOpenVPNNativeController(test func() bool) error {
	return waitOpenVPNNativeControllerFor(15*time.Second, test)
}
func waitOpenVPNNativeControllerFor(timeout time.Duration, test func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if test() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for native OpenVPN state")
}
func openVPNNativeControllerPID(configPath string) int {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && strings.Contains(strings.ReplaceAll(string(raw), "\x00", " "), configPath) {
			pid, _ := strconv.Atoi(entry.Name())
			return pid
		}
	}
	return 0
}
func assertOpenVPNNativeControllerTraffic(t *testing.T, ns string) {
	t.Helper()
	// Verify actual packets traverse the tunnel while tolerating a single lost
	// echo packet; ping still fails if the tunnel delivers no replies at all.
	output, err := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "3", "-i", "0.2", "-W", "2", "10.211.0.1").CombinedOutput()
	if err != nil {
		t.Fatalf("OpenVPN tunnel traffic failed: %v: %s", err, output)
	}
}
func openVPNNativeControllerClientTunIndex(t *testing.T, ns, dev string) int {
	t.Helper()
	raw, err := exec.Command("ip", "netns", "exec", ns, "ip", "-o", "link", "show", "dev", dev).CombinedOutput()
	if err != nil {
		t.Fatalf("client TUN %s missing: %v: %s", dev, err, raw)
	}
	var index int
	if _, err := fmt.Sscanf(string(raw), "%d:", &index); err != nil {
		t.Fatal(err)
	}
	return index
}
func assertOpenVPNNativeControllerClientTun(t *testing.T, ns, dev string, want int) {
	t.Helper()
	if got := openVPNNativeControllerClientTunIndex(t, ns, dev); got != want {
		t.Fatalf("unrelated client tunnel changed: before=%d after=%d", want, got)
	}
}
func assertOpenVPNNativeControllerStable(t *testing.T, config string, pid int, tun string, tunIndex int, dataDir, ns, client string, clientIndex int) {
	t.Helper()
	if got := openVPNNativeControllerPID(config); got != pid {
		t.Fatalf("OpenVPN daemon changed: before=%d after=%d", pid, got)
	}
	iface, err := net.InterfaceByName(tun)
	if err != nil || iface.Index != tunIndex {
		t.Fatalf("server TUN changed: before=%d after=%v err=%v", tunIndex, iface, err)
	}
	openVPNNativeControllerClients(t, dataDir, "alice")
	assertOpenVPNNativeControllerTraffic(t, ns)
	assertOpenVPNNativeControllerClientTun(t, ns, client, clientIndex)
}
func openVPNNativeControllerReceipt(policyPath string, revision int64, operationID string) string {
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		return ""
	}
	var snapshot struct {
		Revision   uint64 `json:"revision"`
		NativeJSON string `json:"native_json"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Revision < uint64(revision) || !strings.Contains(snapshot.NativeJSON, "bob") {
		return ""
	}
	receiptHash := sha256.Sum256([]byte(operationID))
	receiptPath := filepath.Join(filepath.Dir(filepath.Dir(policyPath)), "openvpn", "mutation-results", fmt.Sprintf("%x.json", receiptHash[:]))
	receipt, _ := os.ReadFile(receiptPath)
	return string(receipt)
}
