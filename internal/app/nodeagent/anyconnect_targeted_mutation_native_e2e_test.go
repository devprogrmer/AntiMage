package nodeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// This test is opt-in and is run by the privileged native AnyConnect driver. It
// exercises the production NodeAgent RPC handlers against a real ocserv daemon
// and a real OpenConnect client inside the driver's network namespace.
func TestAnyConnectTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_E2E") != "1" {
		t.Skip("requires the authorized privileged AnyConnect native E2E driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native AnyConnect mutation E2E requires Linux root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("native AnyConnect mutation E2E requires /dev/net/tun: %v", err)
	}
	openconnectPath, err := exec.LookPath("openconnect")
	if err != nil {
		t.Fatalf("native AnyConnect mutation E2E requires openconnect: %v", err)
	}
	vpnScript := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_VPN_SCRIPT"))
	if vpnScript == "" {
		vpnScript = "/usr/share/vpnc-scripts/vpnc-script"
	}
	if _, err := os.Stat(vpnScript); err != nil {
		t.Fatalf("native AnyConnect mutation E2E requires vpnc-script %q: %v", vpnScript, err)
	}
	ns := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_NS"))
	if ns == "" {
		t.Fatal("ANTIMAGE_ANYCONNECT_MUTATION_NS is required")
	}
	if output, err := exec.Command("ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		t.Fatalf("authorized network namespace %q is unavailable: %v: %s", ns, err, output)
	}
	// Bob runs in a second namespace so his vpnc-script cannot rewrite the
	// unrelated Alice client's default route while we assert traffic continuity.
	ns2 := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_NS2"))
	if ns2 == "" {
		t.Fatal("ANTIMAGE_ANYCONNECT_MUTATION_NS2 is required")
	}
	if output, err := exec.Command("ip", "netns", "exec", ns2, "true").CombinedOutput(); err != nil {
		t.Fatalf("authorized second network namespace %q is unavailable: %v: %s", ns2, err, output)
	}
	root := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_ROOT"))
	if root == "" {
		t.Fatal("ANTIMAGE_ANYCONNECT_MUTATION_ROOT is required so native evidence is retained")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			raw, _ := os.ReadFile(filepath.Join(root, entry.Name()))
			t.Logf("native AnyConnect evidence %s:\n%s", entry.Name(), raw)
		}
	})
	serverEndpoint := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_ENDPOINT"))
	if serverEndpoint == "" {
		t.Fatal("ANTIMAGE_ANYCONNECT_MUTATION_ENDPOINT is required")
	}
	serverEndpoint2 := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_ENDPOINT2"))
	if serverEndpoint2 == "" {
		t.Fatal("ANTIMAGE_ANYCONNECT_MUTATION_ENDPOINT2 is required")
	}
	port, err := strconv.Atoi(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_PORT"))
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid native AnyConnect mutation port: %q", os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_PORT"))
	}
	cert, err := readNativeCertEnv("ANTIMAGE_ANYCONNECT_MUTATION_CERT")
	if err != nil {
		t.Fatal(err)
	}
	key, err := readNativeCertEnv("ANTIMAGE_ANYCONNECT_MUTATION_KEY")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := anyConnectNativeCertPin(strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_MUTATION_CERT")))
	if err != nil {
		t.Fatalf("compute ocserv certificate pin: %v", err)
	}

	dataDir := filepath.Join(root, "node")
	inbound := anyConnectRuntimeInbound{
		Tag: "native-anyconnect-mutation", Port: port,
		Settings: map[string]any{
			"server_certificate": cert, "server_key": key,
			"ipv4_pool_cidr": "10.71.0.0/24",
			"udp_enabled":    false,
			// Match the production panel defaults so the real OpenConnect
			// client is not torn down by the renderer's 1-second fallback.
			"dpd": 60, "mobile_dpd": 60, "keepalive": 60,
		},
		Users: []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "alice-native-pass", Status: "active"}},
	}
	server := New(Config{DataDir: dataDir})
	t.Cleanup(server.stopAllAnyConnectRuntimes)
	listener := bufconn.Listen(1 << 20)
	var syncConfigCalls atomic.Int32
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncConfigCalls.Add(1)
		}
		return handler(ctx, req)
	}))
	nodev1.RegisterNodeRuntimeServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := grpc.DialContext(dialCtx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	dialCancel()
	if err != nil {
		t.Fatalf("connect native NodeAgent RPC test transport: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rpc := nodev1.NewNodeRuntimeServiceClient(conn)

	prepared, err := server.prepareAnyConnectInbound(inbound, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.startAnyConnectInbound(prepared); err != nil {
		t.Fatal(err)
	}
	socketPath := prepared.Files.ControlSocket
	if err := waitForAnyConnectMutationNative(func() bool {
		_, queryErr := anyConnectNativeSessions(socketPath)
		return queryErr == nil
	}); err != nil {
		t.Fatalf("wait for native occtl socket %q: %v; NodeAgent runtime logs:\n%s", socketPath, err, strings.Join(server.snapshotLogs(), "\n"))
	}
	server.mu.Lock()
	runtimeBefore := server.anyConnectRuntimes[inbound.Tag]
	if runtimeBefore == nil || runtimeBefore.cmd == nil || runtimeBefore.cmd.Process == nil {
		server.mu.Unlock()
		t.Fatal("managed native ocserv process is absent")
	}
	pidBefore := runtimeBefore.cmd.Process.Pid
	server.mu.Unlock()

	baseNative, err := json.Marshal(nativeRuntimePayload{AnyConnectInbounds: []anyConnectRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(baseNative), DesiredRevision: 10}); err != nil {
		t.Fatal(err)
	}

	alice := startAnyConnectMutationClient(t, root, ns, openconnectPath, vpnScript, pin, serverEndpoint, "alice", "alice-native-pass", "vpn-alice")
	waitAnyConnectMutationSessions(t, socketPath, "alice")
	assertAnyConnectMutationTraffic(t, ns, alice)

	// A failed durable desired-state write must undo staged files and leave the
	// already-running daemon and unrelated session intact.
	bobV1 := anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-native-v1", Status: "active"}
	policyPath := server.runtimePolicyPath()
	policyBefore, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read durable AnyConnect policy before persistence-failure test: %v", err)
	}
	policyInfo, err := os.Stat(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(policyPath); err != nil {
		t.Fatalf("prepare real runtime-policy write failure: %v", err)
	}
	if err := os.Mkdir(policyPath, 0700); err != nil {
		t.Fatalf("prepare runtime policy path obstruction: %v", err)
	}
	policyObstructed := true
	restorePolicy := func() error {
		if !policyObstructed {
			return nil
		}
		if err := os.Remove(policyPath); err != nil {
			return err
		}
		if err := atomicWriteFile(policyPath, policyBefore, policyInfo.Mode().Perm()); err != nil {
			return err
		}
		policyObstructed = false
		return nil
	}
	defer func() {
		if err := restorePolicy(); err != nil {
			t.Errorf("restore durable runtime policy after injected filesystem failure: %v", err)
		}
	}()
	rollbackRequest := anyConnectMutationRequest("native-add-rollback", "add", 11, bobV1)
	rollbackRequest.InboundTag = inbound.Tag
	_, mutationErr := rpc.AddUser(context.Background(), rollbackRequest)
	if err := restorePolicy(); err != nil {
		t.Fatalf("restore previous durable runtime policy: %v", err)
	}
	if mutationErr == nil {
		t.Fatal("persistence failure was not surfaced")
	}
	policyAfter, err := os.ReadFile(policyPath)
	if err != nil || string(policyAfter) != string(policyBefore) {
		t.Fatalf("failed mutation changed durable policy snapshot: err=%v", err)
	}
	assertAnyConnectMutationRollback(t, server, inbound.Tag, 10, prepared, "bob")
	assertAnyConnectMutationSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, socketPath, "alice")
	assertAnyConnectMutationClientTun(t, ns, alice.devName, alice.tunIndex)

	// A canceled waiter must not stage files or change policy.
	lifecycleGate := server.runtimeLifecycleCoordinator().gate(inbound.Tag)
	lockEntered, releaseLock := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseInbound := func() { releaseOnce.Do(func() { close(releaseLock) }) }
	t.Cleanup(releaseInbound)
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- server.runtimeLifecycleCoordinator().runMutation(context.Background(), inbound.Tag, func() error {
			close(lockEntered)
			<-releaseLock
			return nil
		})
	}()
	<-lockEntered
	waitCtx, cancel := context.WithCancel(context.Background())
	rpcDone := make(chan error, 1)
	cancelRequest := anyConnectMutationRequest("native-add-cancel", "add", 11, bobV1)
	cancelRequest.InboundTag = inbound.Tag
	go func() {
		_, callErr := rpc.AddUser(waitCtx, cancelRequest)
		rpcDone <- callErr
	}()
	if err := waitForAnyConnectMutationNative(func() bool {
		lifecycleGate.mu.Lock()
		defer lifecycleGate.mu.Unlock()
		return len(lifecycleGate.waiters) == 1
	}); err != nil {
		cancel()
		t.Fatalf("targeted mutation did not queue behind the held lifecycle lock: %v", err)
	}
	cancel()
	waitErr := <-rpcDone
	releaseInbound()
	if err := <-lockDone; err != nil {
		t.Fatalf("release native per-inbound lifecycle lock: %v", err)
	}
	if waitErr == nil || status.Code(waitErr) != codes.Canceled {
		t.Fatalf("mutation lock wait error = %v, want canceled", waitErr)
	}
	lifecycleGate.mu.Lock()
	waitersAfterCancel := len(lifecycleGate.waiters)
	lifecycleGate.mu.Unlock()
	if waitersAfterCancel != 0 {
		t.Fatalf("canceled targeted mutation leaked %d lifecycle waiter(s)", waitersAfterCancel)
	}
	assertAnyConnectMutationRollback(t, server, inbound.Tag, 10, prepared, "bob")

	add := anyConnectMutationRequest("native-add-bob-11", "add", 11, bobV1)
	add.InboundTag = inbound.Tag
	if response, err := rpc.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("native AddUser response=%v err=%v", response, err)
	}
	// Simulate a lost ACK by replaying exactly the same operation envelope.
	if response, err := rpc.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("native ACK-loss retry response=%v err=%v", response, err)
	}
	conflicting := anyConnectMutationRequest("native-add-bob-11", "add", 11,
		anyConnectRuntimeUser{UserID: 3, Username: "mallory", Password: "wrong", Status: "active"})
	conflicting.InboundTag = inbound.Tag
	if _, err := rpc.AddUser(context.Background(), conflicting); err == nil {
		t.Fatal("reused operation id with a different mutation was accepted")
	}
	if err := waitForAnyConnectMutationNative(func() bool { return anyConnectCredentialPresent(prepared, "bob") }); err != nil {
		t.Fatalf("added native AnyConnect credential not visible: %v", err)
	}
	bob := startAnyConnectMutationClient(t, root, ns2, openconnectPath, vpnScript, pin, serverEndpoint2, "bob", "bob-native-v1", "vpn-bob")
	waitAnyConnectMutationSessions(t, socketPath, "alice", "bob")
	assertAnyConnectMutationTraffic(t, ns, alice)
	assertAnyConnectMutationSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, socketPath, "alice", "bob")
	assertAnyConnectMutationClientTun(t, ns, alice.devName, alice.tunIndex)

	bobV2 := bobV1
	bobV2.Password = "bob-native-v2"
	update := anyConnectMutationRequest("native-update-bob-12", "update", 12, bobV2)
	update.InboundTag = inbound.Tag
	if response, err := rpc.UpdateUser(context.Background(), update); err != nil || !response.GetAccepted() {
		t.Fatalf("native UpdateUser response=%v err=%v", response, err)
	}
	bob.stop()
	// Old credentials must be rejected after the update.
	assertAnyConnectMutationAuthRejected(t, root, ns2, openconnectPath, vpnScript, pin, serverEndpoint2, "bob", "bob-native-v1", "old")
	bob = startAnyConnectMutationClient(t, root, ns2, openconnectPath, vpnScript, pin, serverEndpoint2, "bob", "bob-native-v2", "vpn-bob")
	waitAnyConnectMutationSessions(t, socketPath, "alice", "bob")
	assertAnyConnectMutationTraffic(t, ns, alice)
	assertAnyConnectMutationSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, socketPath, "alice", "bob")
	assertAnyConnectMutationClientTun(t, ns, alice.devName, alice.tunIndex)

	remove := &nodev1.RemoveInboundUserRequest{OperationId: "native-remove-bob-13", InboundTag: inbound.Tag,
		Email: fmt.Sprintf("%s2:13:%s", anyConnectRemoveUserPrefix, base64.RawURLEncoding.EncodeToString([]byte("bob")))}
	if response, err := rpc.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("native RemoveUser response=%v err=%v", response, err)
	}
	bob.stop()
	if err := waitForAnyConnectMutationNative(func() bool {
		sessions, e := anyConnectNativeSessions(socketPath)
		return e == nil && !containsAnyConnectMutationSession(sessions, "bob")
	}); err != nil {
		t.Fatalf("removed native user still has a live session: %v", err)
	}
	assertAnyConnectMutationAuthRejected(t, root, ns2, openconnectPath, vpnScript, pin, serverEndpoint2, "bob", "bob-native-v2", "removed")
	assertAnyConnectMutationRollback(t, server, inbound.Tag, 13, prepared, "bob")
	assertAnyConnectMutationTraffic(t, ns, alice)
	assertAnyConnectMutationSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, socketPath, "alice")
	assertAnyConnectMutationClientTun(t, ns, alice.devName, alice.tunIndex)
	if anyConnectCredentialPresent(prepared, "bob") {
		t.Fatal("removed native AnyConnect credential remains in the password file")
	}
	if got := syncConfigCalls.Load(); got != 0 {
		t.Fatalf("targeted AnyConnect mutation invoked SyncConfig %d times", got)
	}

	// Restart/reconciliation: a fresh Server on the same data dir must load the
	// committed policy and never resurrect the removed user.
	restarted := New(Config{DataDir: dataDir})
	t.Cleanup(restarted.stopAllAnyConnectRuntimes)
	current, err := restarted.currentRuntimePolicy()
	if err != nil || current == nil || current.Revision != 13 {
		t.Fatalf("restarted AnyConnect runtime policy=%+v err=%v", current, err)
	}
	payload, err := parseNativeRuntimePayload(current.NativeJSON)
	if err != nil || len(payload.AnyConnectInbounds) != 1 || len(payload.AnyConnectInbounds[0].Users) != 1 || payload.AnyConnectInbounds[0].Users[0].UserID != 1 {
		t.Fatalf("restarted AnyConnect policy resurrected removed user or dropped alice: %+v err=%v", payload.AnyConnectInbounds, err)
	}
	t.Logf("native AnyConnect targeted Add/Update/Remove preserved ocserv PID %d, control socket %s, and Alice session", pidBefore, socketPath)
}

type anyConnectMutationNativeClient struct {
	cmd      *exec.Cmd
	devName  string
	tunIndex int
	log      *os.File
}

func anyConnectNativeSessions(socketPath string) ([]anyConnectLiveSession, error) {
	return anyConnectOfflineQuery(context.Background(), anyConnectUsageRuntimeConfig{InboundTag: "native", SocketPath: socketPath})
}

func containsAnyConnectMutationSession(sessions []anyConnectLiveSession, username string) bool {
	for _, session := range sessions {
		if strings.EqualFold(strings.TrimSpace(session.Username), username) {
			return true
		}
	}
	return false
}

func waitForAnyConnectMutationNative(predicate func() bool) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if predicate() {
		return nil
	}
	return errors.New("condition did not become true before timeout")
}

func waitAnyConnectMutationSessions(t *testing.T, socketPath string, expected ...string) {
	t.Helper()
	var last []anyConnectLiveSession
	err := waitForAnyConnectMutationNative(func() bool {
		sessions, queryErr := anyConnectNativeSessions(socketPath)
		if queryErr != nil {
			return false
		}
		last = sessions
		for _, username := range expected {
			if !containsAnyConnectMutationSession(sessions, username) {
				return false
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("native AnyConnect sessions did not reach %v: %v; last state=%+v", expected, err, last)
	}
}

func assertAnyConnectMutationSnapshot(t *testing.T, server *Server, tag string, runtimeBefore *anyConnectProcess, pidBefore int, socketPath string, expectedUsers ...string) {
	t.Helper()
	server.mu.Lock()
	runtimeAfter := server.anyConnectRuntimes[tag]
	server.mu.Unlock()
	if runtimeAfter != runtimeBefore || runtimeAfter == nil || runtimeAfter.cmd == nil || runtimeAfter.cmd.Process == nil {
		t.Fatalf("native ocserv daemon was replaced by a targeted mutation")
	}
	if runtimeAfter.cmd.Process.Pid != pidBefore {
		t.Fatalf("native ocserv daemon restarted: before PID=%d after=%d", pidBefore, runtimeAfter.cmd.Process.Pid)
	}
	sessions, err := anyConnectNativeSessions(socketPath)
	if err != nil {
		t.Fatalf("read native occtl sessions: %v", err)
	}
	for _, username := range expectedUsers {
		if !containsAnyConnectMutationSession(sessions, username) {
			t.Fatalf("expected native AnyConnect session %q absent: %+v", username, sessions)
		}
	}
}

func assertAnyConnectMutationRollback(t *testing.T, server *Server, tag string, revision uint64, prepared preparedAnyConnectRuntime, username string) {
	t.Helper()
	current, err := server.currentRuntimePolicy()
	if err != nil || current == nil || current.Revision != revision {
		t.Fatalf("runtime policy after rollback=%+v err=%v, want revision %d", current, err, revision)
	}
	if anyConnectCredentialPresent(prepared, username) {
		t.Fatalf("stale credential for rolled-back/removed user %q remains", username)
	}
	if _, err := os.Stat(filepath.Join(prepared.Files.UserConfigDir, username)); !os.IsNotExist(err) {
		t.Fatalf("stale user config for rolled-back/removed user %q remains: %v", username, err)
	}
}

func anyConnectCredentialPresent(prepared preparedAnyConnectRuntime, username string) bool {
	raw, err := os.ReadFile(prepared.Files.PasswordFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), username+":")
}

func startAnyConnectMutationClient(t *testing.T, root, ns, openconnectPath, vpnScript, pin, endpoint, username, password, iface string) *anyConnectMutationNativeClient {
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
		"--script", vpnScript, "--interface="+iface, "https://"+endpoint)
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start native OpenConnect client %s: %v", username, err)
	}
	client := &anyConnectMutationNativeClient{cmd: cmd, devName: iface, log: log}
	t.Cleanup(client.stop)
	if err := waitForAnyConnectMutationNative(func() bool {
		output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-o", "link", "show", "dev", iface).CombinedOutput()
		if err != nil {
			return false
		}
		var index int
		if _, err := fmt.Sscanf(string(output), "%d:", &index); err != nil || index <= 0 {
			return false
		}
		client.tunIndex = index
		return true
	}); err != nil {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("native OpenConnect client %s never created TUN %s: %v\n%s", username, iface, err, raw)
	}
	if err := waitForAnyConnectMutationNative(func() bool {
		output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-4", "-o", "addr", "show", "dev", iface).CombinedOutput()
		return err == nil && strings.Contains(string(output), "inet ")
	}); err != nil {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("native OpenConnect client %s never received an IPv4 address: %v\n%s", username, err, raw)
	}
	return client
}

func (c *anyConnectMutationNativeClient) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	if c.log != nil {
		_ = c.log.Close()
	}
}

func assertAnyConnectMutationClientTun(t *testing.T, ns, devName string, expectedIndex int) {
	t.Helper()
	output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-o", "link", "show", "dev", devName).CombinedOutput()
	if err != nil {
		t.Fatalf("read native client TUN %s: %v: %s", devName, err, output)
	}
	var index int
	if _, err := fmt.Sscanf(string(output), "%d:", &index); err != nil || index != expectedIndex {
		t.Fatalf("unrelated client TUN identity changed: before=%d read=%q err=%v", expectedIndex, output, err)
	}
}

// assertAnyConnectMutationTraffic proves the unrelated Alice tunnel still
// forwards traffic by pinging the ocserv gateway over the client TUN.
func assertAnyConnectMutationTraffic(t *testing.T, ns string, client *anyConnectMutationNativeClient) {
	t.Helper()
	var lastError string
	err := waitForAnyConnectMutationNative(func() bool {
		cmd := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "2", "-W", "2", "10.71.0.1")
		output, err := cmd.CombinedOutput()
		lastError = string(output)
		return err == nil
	})
	if err != nil {
		t.Fatalf("native AnyConnect tunnel traffic on %s failed: %v: %s", client.devName, err, lastError)
	}
}

// assertAnyConnectMutationAuthRejected proves a client with invalid credentials
// cannot establish a tunnel: either the server rejects the request or the
// client never brings up the TUN interface.
func assertAnyConnectMutationAuthRejected(t *testing.T, root, ns, openconnectPath, vpnScript, pin, endpoint, username, password, label string) {
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
		"--script", vpnScript, "--interface="+iface, "https://"+endpoint)
	cmd.Stdin = strings.NewReader(password + "\n")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start rejected native OpenConnect client %s: %v", label, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = log.Close()
	})
	// Wait for either an explicit rejection or a bounded window during which no
	// tunnel interface is created.
	rejected := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(logPath)
		text := string(raw)
		if strings.Contains(text, "401") || strings.Contains(text, "Authentication failed") ||
			strings.Contains(text, "Login failed") || strings.Contains(text, "Cookie is not acceptable") {
			rejected = true
			break
		}
		if output, err := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", iface).CombinedOutput(); err == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("rejected native OpenConnect client %s unexpectedly created tunnel %s: %s\n%s", label, iface, output, text)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !rejected {
		raw, _ := os.ReadFile(logPath)
		t.Fatalf("rejected native OpenConnect client %s produced no explicit authentication rejection\n%s", label, raw)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func anyConnectNativeCertPin(certPath string) (string, error) {
	output, err := exec.Command("openssl", "x509", "-in", certPath, "-pubkey", "-noout").Output()
	if err != nil {
		return "", err
	}
	der, err := runAndCapture(output, "openssl", "pkey", "-pubin", "-outform", "DER")
	if err != nil {
		return "", err
	}
	digest, err := runAndCapture(der, "openssl", "dgst", "-sha256", "-binary")
	if err != nil {
		return "", err
	}
	return "pin-sha256:" + base64.StdEncoding.EncodeToString(digest), nil
}

func runAndCapture(stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(string(stdin))
	return cmd.Output()
}
