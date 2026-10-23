package nodecontroller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	operationapp "github.com/antimage/antimage/internal/app/operations"
	userapp "github.com/antimage/antimage/internal/app/user"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

const nativeAnyConnectTestNodeCapabilities = `["shared_fencing_v1","command_idempotency_v1","config_revision","targeted_user_add_remove_anyconnect","targeted_user_update_anyconnect"]`

// This privileged integration test exercises the production Controller to
// NodeAgent to ocserv path over real gRPC and a real OpenConnect client. It
// never injects capabilities through a test-only shim: the NodeAgent must
// advertise the gated AnyConnect capability for the targeted path to be used.
func TestControllerAnyConnectTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_E2E") != "1" {
		t.Skip("requires the authorized privileged AnyConnect native E2E driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native Controller AnyConnect E2E requires Linux root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("native Controller AnyConnect E2E requires /dev/net/tun: %v", err)
	}
	root := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_CONTROLLER_ROOT"))
	ns := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_NS"))
	ns2 := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_NS2"))
	certPath := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_CERT"))
	keyPath := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_KEY"))
	vpnScript := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_VPN_SCRIPT"))
	if vpnScript == "" {
		vpnScript = "/usr/share/vpnc-scripts/vpnc-script"
	}
	port, err := strconv.Atoi(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_PORT"))
	if root == "" || ns == "" || ns2 == "" || certPath == "" || keyPath == "" || err != nil || port <= 0 {
		t.Fatal("native Controller AnyConnect E2E requires the privileged driver's namespaces, root, certificate, key, and port")
	}
	for _, namespace := range []string{ns, ns2} {
		if output, err := exec.Command("ip", "netns", "exec", namespace, "true").CombinedOutput(); err != nil {
			t.Fatalf("authorized network namespace %q unavailable: %v: %s", namespace, err, output)
		}
	}
	openconnectPath, err := exec.LookPath("openconnect")
	if err != nil {
		t.Fatalf("native Controller AnyConnect E2E requires openconnect: %v", err)
	}
	pin, err := anyConnectControllerCertPin(certPath)
	if err != nil {
		t.Fatalf("compute ocserv certificate pin: %v", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_NODE_APP_DIR", root)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	serverTLS, clientTLS := openVPNNativeControllerTLS(t)
	db, repo, node, runtimeConfig, initialNative, alicePassword := anyConnectNativeControllerDB(t, ctx, root, certPath, keyPath, serverTLS.Certificates[0])
	defer db.Close()
	dataDir := filepath.Join(root, "node-agent")
	agent := nodeagent.New(nodeagent.Config{DataDir: dataDir})
	hello, err := agent.Hello(ctx, &nodev1.HelloRequest{MasterId: "antimage-master"})
	if err != nil {
		t.Fatalf("read production NodeAgent capabilities: %v", err)
	}
	for _, capability := range []string{"targeted_user_add_remove_anyconnect", "targeted_user_update_anyconnect"} {
		if !openVPNNativeControllerHasCapability(hello.GetRuntime().GetCapabilities(), capability) {
			t.Fatalf("production NodeAgent did not advertise gated AnyConnect capability %q", capability)
		}
	}
	listener, grpcServer, address := startOpenVPNNativeControllerAgent(t, agent, serverTLS, true)
	defer func() {
		grpcServer.Stop()
		_ = listener.Close()
		_, _ = agent.StopRuntime(context.Background(), &nodev1.StopRuntimeRequest{})
	}()
	grpcPort := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET port = ? WHERE id = 7`, grpcPort); err != nil {
		t.Fatal(err)
	}
	node.Address, node.Port = "127.0.0.1", grpcPort
	controller := NewController(repo)
	client, err := nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial native NodeAgent: %v", err)
	}
	defer client.Close()
	initialRequest := &nodev1.RuntimeConfigRequest{
		OperationId: "native-anyconnect-initial-100", ConfigJson: runtimeConfig,
		OvRuntimeJson: initialNative, DesiredRevision: 100,
	}
	if err := controller.executeLegacyNodeCommand(legacyConfigEvidence(ctx, runtimeConfig), 7, initialRequest.GetOperationId(), "sync_config", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		initialRequest.Fence = fence
		return client.Runtime().SyncConfig(worker, initialRequest)
	}, nil); err != nil {
		t.Fatalf("Controller initial native AnyConnect sync: %v; NodeAgent logs:\n%s", err, anyConnectNativeControllerLogs(t, ctx, client))
	}
	controlSocket := nodeagent.AnyConnectControlSocketPath(dataDir, "ac-in")
	if err := waitOpenVPNNativeController(func() bool { return anyConnectNativeControllerSocketReady(controlSocket) }); err != nil {
		t.Fatalf("wait for ocserv control socket: %v", err)
	}
	pidBefore, err := anyConnectNativeControllerPID(dataDir, "ac-in")
	if err != nil {
		t.Fatal(err)
	}
	alice := startAnyConnectNativeControllerClient(t, root, ns, openconnectPath, vpnScript, pin, "10.253.0.1", port, "alice", alicePassword, "vpn-alice")
	defer alice.stop()
	if err := waitOpenVPNNativeController(func() bool { return anyConnectNativeControllerHasSession(controlSocket, "alice") }); err != nil {
		t.Fatalf("Alice did not establish a real AnyConnect session: %v", err)
	}
	assertAnyConnectNativeControllerTraffic(t, ns, alice)
	aliceTunIndex := anyConnectNativeControllerClientTunIndex(t, ns, alice.dev)

	var syncCalls atomic.Int32
	var dropAckFor atomic.Value
	dropAckFor.Store("")
	grpcServer.Stop()
	_ = listener.Close()
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncCalls.Add(1)
		}
		response, callErr := handler(ctx, req)
		if mutation, ok := req.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == dropAckFor.Load() && callErr == nil {
			dropAckFor.Store("")
			return nil, status.Error(codes.Unavailable, "simulated connection loss after NodeAgent commit, before ACK")
		}
		return response, callErr
	}
	listener, grpcServer, address = startOpenVPNNativeControllerAgentWithInterceptor(t, agent, serverTLS, interceptor)
	grpcPort = listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET port = ? WHERE id = 7`, grpcPort); err != nil {
		t.Fatal(err)
	}
	node.Address, node.Port = "127.0.0.1", grpcPort
	client, err = nodeclient.Dial(ctx, address.String(), clientTLS, grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial re-registered native NodeAgent: %v", err)
	}
	defer client.Close()
	controller = NewController(repo)

	// Bob is added through the durable Controller queue. The NodeAgent commits
	// the mutation but the Controller loses the first ACK; a restarted
	// Controller replays the same operation id and must not re-apply the change.
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id,username,credential_key,service_id,status,used_traffic,data_limit,expire,device_limit,ip_limit,upload_speed_limit,download_speed_limit) VALUES (8,'bob','1123456789abcdef0123456789abcdef',10,'active',0,0,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO proxies (user_id,type) VALUES (8,'anyconnect')`); err != nil {
		t.Fatal(err)
	}
	bobPassword, err := userapp.AnyConnectPasswordFromCredentialKey("1123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := queueOpenVPNNativeControllerOperation(ctx, db, 101, "add_user", 8); err != nil {
		t.Fatal(err)
	}
	dropAckFor.Store("add_user-101-ac-in")
	result, err := controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Retrying != 1 || result.Done != 0 {
		t.Fatalf("lost-ACK first delivery result=%+v err=%v", result, err)
	}
	if err := waitOpenVPNNativeController(func() bool { return anyConnectNativeControllerCredential(dataDir, "ac-in", "bob") }); err != nil {
		t.Fatalf("NodeAgent did not persist AnyConnect AddUser before lost ACK: %v", err)
	}
	if got, err := anyConnectNativeControllerPID(dataDir, "ac-in"); err != nil || got != pidBefore {
		t.Fatalf("AnyConnect AddUser restarted ocserv: before=%d after=%d err=%v", pidBefore, got, err)
	}
	passwordPath := filepath.Join(dataDir, "anyconnect", openVPNNativeDirName("ac-in"), "ocpasswd")
	passwordBeforeReplay, err := os.Stat(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE node_operations SET status='pending', updated_at=? WHERE id=101`, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	controller = NewController(repo)
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 || result.Retrying != 0 {
		t.Fatalf("lost-ACK replay after Controller restart result=%+v err=%v", result, err)
	}
	passwordAfterReplay, err := os.Stat(passwordPath)
	if err != nil || !passwordAfterReplay.ModTime().Equal(passwordBeforeReplay.ModTime()) {
		t.Fatalf("lost-ACK replay rewrote already-committed AnyConnect credentials: before=%v after=%v err=%v", passwordBeforeReplay.ModTime(), passwordAfterReplay, err)
	}
	if got := syncCalls.Load(); got != 0 {
		t.Fatalf("targeted AnyConnect path invoked SyncConfig %d times", got)
	}
	bob := startAnyConnectNativeControllerClient(t, root, ns2, openconnectPath, vpnScript, pin, "10.254.0.1", port, "bob", bobPassword, "vpn-bob")
	defer bob.stop()
	if err := waitOpenVPNNativeController(func() bool { return anyConnectNativeControllerHasSession(controlSocket, "bob") }); err != nil {
		t.Fatalf("Bob did not establish after Controller AddUser: %v", err)
	}
	assertAnyConnectNativeControllerClients(t, controlSocket, "alice", "bob")
	assertAnyConnectNativeControllerTraffic(t, ns, alice)
	assertAnyConnectNativeControllerClientTun(t, ns, alice.dev, aliceTunIndex)
	if got := anyConnectNativeControllerReceipt(dataDir, 101, "add_user-101-ac-in"); got == "" {
		t.Fatal("the NodeAgent did not retain the idempotency receipt for the ACK-lost AnyConnect AddUser")
	}
	if got := openVPNNativeControllerOperationStatus(t, db, 101); got != "done" {
		t.Fatalf("controller did not durably persist the replayed AnyConnect ACK: operation status=%q", got)
	}

	// Update through the Controller queue, then remove. Alice's session and the
	// ocserv daemon PID must persist throughout.
	if _, err := db.ExecContext(ctx, `UPDATE users SET credential_key='2123456789abcdef0123456789abcdef' WHERE id=8`); err != nil {
		t.Fatal(err)
	}
	bobPasswordV2, err := userapp.AnyConnectPasswordFromCredentialKey("2123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := queueOpenVPNNativeControllerOperation(ctx, db, 102, "update_user", 8); err != nil {
		t.Fatal(err)
	}
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 {
		t.Fatalf("Controller UpdateUser result=%+v err=%v", result, err)
	}
	bob.stop()
	if err := waitOpenVPNNativeController(func() bool { return !anyConnectNativeControllerHasSession(controlSocket, "bob") }); err != nil {
		t.Fatalf("removed Bob session persisted after Update: %v", err)
	}
	assertAnyConnectNativeControllerAuthRejected(t, root, ns2, openconnectPath, vpnScript, pin, "10.254.0.1", "4434", port, "bob", bobPassword, "anyconnect-old")
	bob = startAnyConnectNativeControllerClient(t, root, ns2, openconnectPath, vpnScript, pin, "10.254.0.1", port, "bob", bobPasswordV2, "vpn-bob")
	if err := waitOpenVPNNativeController(func() bool { return anyConnectNativeControllerHasSession(controlSocket, "bob") }); err != nil {
		t.Fatalf("Bob did not re-establish with the updated credential: %v", err)
	}
	assertAnyConnectNativeControllerTraffic(t, ns, alice)
	assertAnyConnectNativeControllerClientTun(t, ns, alice.dev, aliceTunIndex)

	if err := queueOpenVPNNativeControllerOperation(ctx, db, 103, "remove_user", 8); err != nil {
		t.Fatal(err)
	}
	result, err = controller.ProcessRuntimeUserOperations(ctx, ProcessUserOperationsRequest{UserID: 8, Limit: 10})
	if err != nil || result.Done != 1 {
		t.Fatalf("Controller RemoveUser result=%+v err=%v", result, err)
	}
	bob.stop()
	if err := waitOpenVPNNativeController(func() bool { return !anyConnectNativeControllerHasSession(controlSocket, "bob") }); err != nil {
		t.Fatalf("removed Bob session persisted after Remove: %v", err)
	}
	assertAnyConnectNativeControllerAuthRejected(t, root, ns2, openconnectPath, vpnScript, pin, "10.254.0.1", "4434", port, "bob", bobPasswordV2, "anyconnect-removed")
	if anyConnectNativeControllerCredential(dataDir, "ac-in", "bob") {
		t.Fatal("removed AnyConnect credential remains in the ocserv password file")
	}
	if got, err := anyConnectNativeControllerPID(dataDir, "ac-in"); err != nil || got != pidBefore {
		t.Fatalf("AnyConnect mutations restarted ocserv: before=%d after=%d err=%v", pidBefore, got, err)
	}
	assertAnyConnectNativeControllerTraffic(t, ns, alice)
	assertAnyConnectNativeControllerClientTun(t, ns, alice.dev, aliceTunIndex)
	if got := syncCalls.Load(); got != 0 {
		t.Fatalf("targeted AnyConnect Add/Update/Remove invoked SyncConfig %d times", got)
	}

	// Restart/reconciliation: a fresh NodeAgent over the same data dir must load
	// the committed policy and never resurrect the removed user.
	restarted := nodeagent.New(nodeagent.Config{DataDir: dataDir})
	_ = restarted
	policyRaw, err := os.ReadFile(filepath.Join(dataDir, "accounting", "runtime-policy.json"))
	if err != nil {
		t.Fatalf("read restarted AnyConnect runtime policy: %v", err)
	}
	var snapshot struct {
		Revision   uint64 `json:"revision"`
		NativeJSON string `json:"native_json"`
	}
	if err := json.Unmarshal(policyRaw, &snapshot); err != nil || snapshot.Revision != 103 {
		t.Fatalf("restarted AnyConnect runtime policy revision=%d err=%v", snapshot.Revision, err)
	}
	if strings.Contains(snapshot.NativeJSON, `"user_id":8`) {
		t.Fatalf("restarted AnyConnect policy resurrected the removed user: %s", snapshot.NativeJSON)
	}
	t.Logf("native Controller -> NodeAgent -> ocserv targeted Add/Update/Remove preserved ocserv PID %d and Alice session", pidBefore)
}

func anyConnectControllerCertPin(certPath string) (string, error) {
	pubkey, err := exec.Command("openssl", "x509", "-in", certPath, "-pubkey", "-noout").Output()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("openssl", "pkey", "-pubin", "-outform", "DER")
	cmd.Stdin = strings.NewReader(string(pubkey))
	der, err := cmd.Output()
	if err != nil {
		return "", err
	}
	cmd = exec.Command("openssl", "dgst", "-sha256", "-binary")
	cmd.Stdin = strings.NewReader(string(der))
	digest, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return "pin-sha256:" + base64.StdEncoding.EncodeToString(digest), nil
}

func anyConnectNativeControllerDB(t *testing.T, ctx context.Context, root, certPath, keyPath string, identity tls.Certificate) (*sql.DB, Repository, NodeRow, string, string, string) {
	t.Helper()
	cert, key := openVPNNativeControllerTLSPEM(t, identity)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "anyconnect-controller-native.db")+"?_pragma=busy_timeout(30000)")
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
CREATE TABLE inbounds (tag TEXT PRIMARY KEY,uplink INTEGER DEFAULT 0,downlink INTEGER DEFAULT 0,usage_coefficient REAL DEFAULT 1);
CREATE TABLE wireguard_peer_addresses (inbound_tag TEXT,user_id INTEGER,pool TEXT,server_address TEXT,address TEXT,PRIMARY KEY(inbound_tag,user_id),UNIQUE(inbound_tag,address));
CREATE TABLE node_wireguard_usage_reflection (node_id INTEGER,user_id INTEGER,batch_id TEXT,ledger_json TEXT NULL,updated_at DATETIME,PRIMARY KEY(node_id,user_id));
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
	config := nativeAnyConnectControllerNodeConfig(t, certPath, keyPath)
	alicePassword, err := userapp.AnyConnectPasswordFromCredentialKey("0123456789abcdef0123456789abcdef")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tls VALUES (1,?,?)`, []any{string(cert), string(key)}},
		{`INSERT INTO xray_config VALUES (1,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{config}},
		{`INSERT INTO nodes VALUES (7,'native-node','127.0.0.1',0,0,'connected','','',?,?,'custom',?,CURRENT_TIMESTAMP,1,?,100,100,'connected','running',CURRENT_TIMESTAMP)`, []any{string(cert), string(key), config, nativeAnyConnectTestNodeCapabilities}},
		{`INSERT INTO users VALUES (7,'alice','0123456789abcdef0123456789abcdef',10,'active',0,0,0,0,0,0,0)`, nil},
		{`INSERT INTO proxies VALUES (7,'anyconnect')`, nil},
		{`INSERT INTO hosts VALUES (1,'ac-in',0)`, nil},
		{`INSERT INTO service_hosts VALUES (10,1)`, nil},
		{`INSERT INTO inbounds VALUES ('ac-in',0,0,1)`, nil},
	} {
		if _, err := db.ExecContext(ctx, seed.query, seed.args...); err != nil {
			_ = db.Close()
			t.Fatalf("seed AnyConnect Controller E2E database: %v", err)
		}
	}
	repo := NewRepository(db, "sqlite")
	node, err := repo.Node(ctx, 7)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	settings := map[string]any{
		"server_certificate": readNativeOpenVPNText(t, certPath),
		"server_key":         readNativeOpenVPNText(t, keyPath),
		"ipv4_pool_cidr":     "10.71.0.0/24",
		"udp_enabled":        false,
		"accounting_enabled": false,
		"dpd":                60, "mobile_dpd": 60, "keepalive": 60,
	}
	users, err := repo.remoteAccessUsers(ctx, "ac-in", []int64{10}, "10.71.0.0/24", "anyconnect")
	if err != nil || len(users) != 1 {
		_ = db.Close()
		t.Fatalf("load native AnyConnect user: users=%+v err=%v", users, err)
	}
	native, err := json.Marshal(map[string]any{"anyconnect_inbounds": []any{map[string]any{"tag": "ac-in", "port": anyConnectNativeControllerPort(), "settings": settings, "users": users}}})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, repo, node, `{"inbounds":[]}`, string(native), alicePassword
}

func nativeAnyConnectControllerNodeConfig(t *testing.T, certPath, keyPath string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"tag": "ac-in", "protocol": "anyconnect", "port": anyConnectNativeControllerPort(), "targets": []any{"node:7"}, "settings": map[string]any{"ipv4_pool_cidr": "10.71.0.0/24", "server_certificate": readNativeOpenVPNText(t, certPath), "server_key": readNativeOpenVPNText(t, keyPath), "udp_enabled": false, "accounting_enabled": false, "dpd": 60, "mobile_dpd": 60, "keepalive": 60}}}})
	return string(raw)
}

func anyConnectNativeControllerPort() int {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_PORT"))); err == nil && value > 0 {
		return value
	}
	return 4434
}

func anyConnectNativeControllerSocketReady(socketPath string) bool {
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}
	_, err := exec.Command("occtl", "-s", socketPath, "--json", "show", "users").Output()
	return err == nil
}

func anyConnectNativeControllerSessions(socketPath string) []anyConnectNativeSession {
	output, err := exec.Command("occtl", "-s", socketPath, "--json", "show", "users").Output()
	if err != nil {
		return nil
	}
	return parseAnyConnectNativeSessions(output)
}

// parseAnyConnectNativeSessions accepts both occtl shapes: a bare JSON array of
// sessions, or an object carrying a "users"/"sessions" list. An empty ledger is
// represented as an empty slice so readiness and continuity checks are exact.
func parseAnyConnectNativeSessions(raw []byte) []anyConnectNativeSession {
	var direct []anyConnectNativeSession
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct
	}
	var wrapper struct {
		Users    []anyConnectNativeSession `json:"users"`
		Sessions []anyConnectNativeSession `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil
	}
	if wrapper.Users != nil {
		return wrapper.Users
	}
	return wrapper.Sessions
}

type anyConnectNativeSession struct {
	Username string `json:"username"`
}

func anyConnectNativeControllerHasSession(socketPath, username string) bool {
	for _, session := range anyConnectNativeControllerSessions(socketPath) {
		if strings.EqualFold(strings.TrimSpace(session.Username), username) {
			return true
		}
	}
	return false
}

func anyConnectNativeControllerCredential(dataDir, tag, username string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, "anyconnect", openVPNNativeDirName(tag), "ocpasswd"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), username+":")
}

// anyConnectNativeControllerReceipt reads the durable AnyConnect mutation
// receipt the NodeAgent writes once a targeted operation has been committed.
// The receipt path mirrors the production layout under the AnyConnect subtree
// rather than the OpenVPN one, so a Controller that loses the ACK can prove it
// did not re-apply the operation on replay.
func anyConnectNativeControllerReceipt(dataDir string, revision int64, operationID string) string {
	policyRaw, err := os.ReadFile(filepath.Join(dataDir, "accounting", "runtime-policy.json"))
	if err != nil {
		return ""
	}
	var snapshot struct {
		Revision   uint64 `json:"revision"`
		NativeJSON string `json:"native_json"`
	}
	if json.Unmarshal(policyRaw, &snapshot) != nil || snapshot.Revision < uint64(revision) {
		return ""
	}
	receiptHash := sha256.Sum256([]byte(operationID))
	receiptPath := filepath.Join(dataDir, "anyconnect", "mutation-results", fmt.Sprintf("%x.json", receiptHash[:]))
	receipt, _ := os.ReadFile(receiptPath)
	return string(receipt)
}

// anyConnectNativeControllerLogs streams the NodeAgent's bounded runtime log
// buffer so an ocserv startup failure surfaces its stderr instead of just the
// gRPC status.
func anyConnectNativeControllerLogs(t *testing.T, ctx context.Context, client *nodeclient.Client) string {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := client.Logs().StreamLogs(streamCtx, &nodev1.StreamLogsRequest{StreamId: "anyconnect-native", MaxLines: 200})
	if err != nil {
		return fmt.Sprintf("<log stream unavailable: %v>", err)
	}
	var lines []string
	for {
		line, err := stream.Recv()
		if err != nil {
			break
		}
		lines = append(lines, line.GetLine())
	}
	if len(lines) == 0 {
		return "<no NodeAgent log lines captured>"
	}
	return strings.Join(lines, "\n")
}

func anyConnectNativeControllerPID(dataDir, tag string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "anyconnect", openVPNNativeDirName(tag), "ocserv.pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

type anyConnectNativeControllerClient struct {
	cmd *exec.Cmd
	dev string
	log *os.File
}

func startAnyConnectNativeControllerClient(t *testing.T, root, ns, openconnectPath, vpnScript, pin, endpoint string, port int, username, password, iface string) *anyConnectNativeControllerClient {
	t.Helper()
	name := username + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	logPath := filepath.Join(root, name+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, openconnectPath,
		"--protocol=anyconnect", "--user="+username, "--passwd-on-stdin",
		"--reconnect-timeout=1", "--servercert", pin, "--no-dtls",
		"--script", vpnScript, "--interface="+iface, fmt.Sprintf("https://%s:%d", endpoint, port))
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start native OpenConnect client %s: %v", username, err)
	}
	client := &anyConnectNativeControllerClient{cmd: cmd, dev: iface, log: log}
	t.Cleanup(client.stop)
	if err := waitOpenVPNNativeController(func() bool {
		output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-4", "-o", "addr", "show", "dev", iface).CombinedOutput()
		return err == nil && strings.Contains(string(output), "inet ")
	}); err != nil {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("native OpenConnect client %s never configured %s: %v\n%s", username, iface, err, raw)
	}
	return client
}

func (c *anyConnectNativeControllerClient) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	if c.log != nil {
		_ = c.log.Close()
	}
}

func assertAnyConnectNativeControllerClients(t *testing.T, socketPath string, users ...string) {
	t.Helper()
	for _, username := range users {
		if !anyConnectNativeControllerHasSession(socketPath, username) {
			t.Fatalf("expected native AnyConnect session %q absent: %+v", username, anyConnectNativeControllerSessions(socketPath))
		}
	}
}

func assertAnyConnectNativeControllerTraffic(t *testing.T, ns string, client *anyConnectNativeControllerClient) {
	t.Helper()
	var lastError string
	err := waitOpenVPNNativeController(func() bool {
		cmd := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "2", "-W", "2", "10.71.0.1")
		output, err := cmd.CombinedOutput()
		lastError = string(output)
		return err == nil
	})
	if err != nil {
		t.Fatalf("native AnyConnect tunnel traffic on %s failed: %v: %s", client.dev, err, lastError)
	}
}

func anyConnectNativeControllerClientTunIndex(t *testing.T, ns, dev string) int {
	t.Helper()
	output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-o", "link", "show", "dev", dev).CombinedOutput()
	if err != nil {
		t.Fatalf("read native client TUN %s: %v: %s", dev, err, output)
	}
	var index int
	if _, err := fmt.Sscanf(string(output), "%d:", &index); err != nil || index <= 0 {
		t.Fatalf("parse native client TUN index from %q: %v", output, err)
	}
	return index
}

func assertAnyConnectNativeControllerClientTun(t *testing.T, ns, dev string, want int) {
	t.Helper()
	if got := anyConnectNativeControllerClientTunIndex(t, ns, dev); got != want {
		t.Fatalf("unrelated client TUN identity changed: before=%d after=%d", want, got)
	}
}

// assertAnyConnectNativeControllerAuthRejected proves an invalid credential
// cannot establish a tunnel: either an explicit rejection appears or no client
// TUN is created within the bounded window.
func assertAnyConnectNativeControllerAuthRejected(t *testing.T, root, ns, openconnectPath, vpnScript, pin, endpoint, _ string, port int, username, password, label string) {
	t.Helper()
	name := "reject-" + label + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	logPath := filepath.Join(root, name+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	iface := "rj" + fmt.Sprintf("%x", []byte(label+username))[:8]
	cmd := exec.Command("ip", "netns", "exec", ns, openconnectPath,
		"--protocol=anyconnect", "--user="+username, "--passwd-on-stdin",
		"--reconnect-timeout=1", "--servercert", pin, "--no-dtls",
		"--script", vpnScript, "--interface="+iface, fmt.Sprintf("https://%s:%d", endpoint, port))
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start rejected native OpenConnect client %s: %v", label, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = log.Close()
	}()
	rejected := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(logPath)
		text := string(raw)
		if strings.Contains(text, "401") || strings.Contains(text, "Login failed") ||
			strings.Contains(text, "Authentication failed") || strings.Contains(text, "Cookie is not acceptable") {
			rejected = true
			break
		}
		if output, err := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", iface).CombinedOutput(); err == nil {
			t.Fatalf("rejected native OpenConnect client %s unexpectedly created tunnel %s: %s\n%s", label, iface, output, text)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !rejected {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("rejected native OpenConnect client %s produced no explicit authentication rejection\n%s", label, raw)
	}
}
