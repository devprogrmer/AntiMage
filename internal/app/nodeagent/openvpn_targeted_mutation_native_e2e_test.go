package nodeagent

import (
	"context"
	"crypto/sha256"
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

// This test is opt-in and is run by the privileged native OpenVPN driver. It
// exercises the production NodeAgent RPC handlers against a real OpenVPN
// daemon and two real clients in the driver's network namespace.
func TestOpenVPNTargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_OPENVPN_MUTATION_E2E") != "1" {
		t.Skip("requires the authorized privileged OpenVPN native E2E driver")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("native OpenVPN mutation E2E requires Linux root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("native OpenVPN mutation E2E requires /dev/net/tun: %v", err)
	}
	openvpnPath, err := exec.LookPath("openvpn")
	if err != nil {
		t.Fatalf("native OpenVPN mutation E2E requires openvpn: %v", err)
	}
	ns := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_NS"))
	if ns == "" {
		t.Fatal("ANTIMAGE_OPENVPN_MUTATION_NS is required")
	}
	if output, err := exec.Command("ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		t.Fatalf("authorized network namespace %q is unavailable: %v: %s", ns, err, output)
	}

	root := strings.TrimSpace(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_ROOT"))
	if root == "" {
		t.Fatal("ANTIMAGE_OPENVPN_MUTATION_ROOT is required so native evidence is retained")
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
			t.Logf("native OpenVPN evidence %s:\n%s", entry.Name(), raw)
		}
	})
	ca, err := readNativeCertEnv("ANTIMAGE_OPENVPN_MUTATION_CA")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := readNativeCertEnv("ANTIMAGE_OPENVPN_MUTATION_CERT")
	if err != nil {
		t.Fatal(err)
	}
	key, err := readNativeCertEnv("ANTIMAGE_OPENVPN_MUTATION_KEY")
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(os.Getenv("ANTIMAGE_OPENVPN_MUTATION_PORT"))
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid native OpenVPN mutation port: %q", os.Getenv("ANTIMAGE_OPENVPN_MUTATION_PORT"))
	}

	dataDir := filepath.Join(root, "node")
	inbound := openVPNRuntimeInbound{
		Tag: "native-mutation", Port: port, Transport: "udp",
		Settings: map[string]any{
			"ca": ca, "server_certificate": cert, "server_key": key,
			"ipv4_pool_cidr": "10.211.0.0/24", "redirect_gateway": false,
			"accounting_enabled": false,
		},
		Users: []openVPNRuntimeUser{{UserID: 1, Username: "alice", VPNUsername: "alice", Password: "alice-native-pass", Status: "active"}},
	}
	server := New(Config{DataDir: dataDir})
	t.Cleanup(server.stopAllOpenVPNRuntimes)
	listener := bufconn.Listen(1 << 20)
	cancelRPCEntered := make(chan struct{}, 1)
	cancelRPCFinished := make(chan error, 1)
	var syncConfigCalls atomic.Int32
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncConfigCalls.Add(1)
		}
		if mutation, ok := req.(*nodev1.InboundUserRequest); ok && mutation.GetOperationId() == "native-add-cancel" {
			cancelRPCEntered <- struct{}{}
			response, err := handler(ctx, req)
			cancelRPCFinished <- err
			return response, err
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
	configPath, err := server.prepareOpenVPNInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.startOpenVPNInbound(inbound.Tag, configPath); err != nil {
		t.Fatal(err)
	}
	tunName := openVPNTunName(inbound.Tag)
	if err := waitForOpenVPNMutationNative(func() bool {
		_, tunErr := net.InterfaceByName(tunName)
		return tunErr == nil
	}); err != nil {
		interfaces, _ := net.Interfaces()
		t.Fatalf("wait for native OpenVPN TUN %q: %v; interfaces=%+v; NodeAgent runtime logs:\n%s", tunName, err, interfaces, strings.Join(server.snapshotLogs(), "\n"))
	}
	managementPath := filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName(inbound.Tag), "management.sock")
	var managementErr error
	if err := waitForOpenVPNMutationNative(func() bool {
		_, managementErr = openVPNMutationManagementStatus("unix", managementPath)
		return managementErr == nil
	}); err != nil {
		t.Fatalf("wait for native OpenVPN management status at %q: %v; last status error: %v; NodeAgent runtime logs:\n%s", managementPath, err, managementErr, strings.Join(server.snapshotLogs(), "\n"))
	}
	server.mu.Lock()
	runtimeBefore := server.openVPNRuntimes[inbound.Tag]
	if runtimeBefore == nil || runtimeBefore.cmd == nil || runtimeBefore.cmd.Process == nil {
		server.mu.Unlock()
		t.Fatal("managed native OpenVPN process is absent")
	}
	pidBefore := runtimeBefore.cmd.Process.Pid
	server.mu.Unlock()
	t.Cleanup(func() {
		server.stopAllOpenVPNRuntimes()
		_ = exec.Command("ip", "link", "delete", "dev", tunName).Run()
	})
	tunBefore, err := net.InterfaceByName(tunName)
	if err != nil {
		t.Fatal(err)
	}
	baseNative, err := json.Marshal(nativeRuntimePayload{OpenVPNInbounds: []openVPNRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(baseNative), DesiredRevision: 10}); err != nil {
		t.Fatal(err)
	}

	caPath := os.Getenv("ANTIMAGE_OPENVPN_MUTATION_CA")
	alice := startOpenVPNMutationClient(t, root, ns, openvpnPath, caPath, port, "alice", "alice-native-pass")
	aliceClientID := waitOpenVPNMutationClients(t, server, inbound.Tag, "alice")
	aliceTunIndex := nativeMutationClientTunIndex(t, ns, alice.devName)
	assertNativeOpenVPNTraffic(t, ns, tunName)
	assertNativeOpenVPNSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, tunName, tunBefore.Index, aliceClientID, "alice")
	assertNativeMutationClientTun(t, ns, alice.devName, aliceTunIndex)

	// A failed durable desired-state write must undo staged files and leave the
	// already-running daemon and unrelated session intact.
	bobV1 := openVPNRuntimeUser{UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-native-v1", IPv4Address: "10.211.0.10", Status: "active"}
	oldAtomicWrite := runtimePolicyAtomicWrite
	runtimePolicyAtomicWrite = func(path string, data []byte) error {
		if filepath.Clean(path) == filepath.Clean(server.runtimePolicyPath()) {
			return errors.New("injected native E2E policy persistence failure")
		}
		return oldAtomicWrite(path, data)
	}
	_, mutationErr := rpc.AddUser(context.Background(), openVPNMutationRequest("native-add-rollback", "add", 11, bobV1))
	runtimePolicyAtomicWrite = oldAtomicWrite
	if mutationErr == nil || !strings.Contains(mutationErr.Error(), "persist OpenVPN user policy") {
		t.Fatalf("persistence failure was not surfaced: %v", mutationErr)
	}
	assertOpenVPNMutationRollback(t, server, inbound.Tag, 10, dataDir, "bob")
	assertNativeOpenVPNSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, tunName, tunBefore.Index, aliceClientID, "alice")
	assertNativeMutationClientTun(t, ns, alice.devName, aliceTunIndex)

	// A timed-out waiter must not stage files or change policy. Holding the
	// same operation lock makes this deterministic without weakening the gate.
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
	lifecycleGate := server.runtimeLifecycleCoordinator().gate(inbound.Tag)
	waitCtx, cancel := context.WithCancel(context.Background())
	rpcDone := make(chan error, 1)
	go func() {
		_, callErr := rpc.AddUser(waitCtx, openVPNMutationRequest("native-add-cancel", "add", 11, bobV1))
		rpcDone <- callErr
	}()
	select {
	case <-cancelRPCEntered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("cancellation RPC did not enter NodeAgent handler")
	}
	if err := waitForOpenVPNMutationNative(func() bool {
		lifecycleGate.mu.Lock()
		defer lifecycleGate.mu.Unlock()
		return len(lifecycleGate.waiters) == 1
	}); err != nil {
		cancel()
		t.Fatalf("targeted mutation did not queue behind the held lifecycle lock: %v", err)
	}
	cancel()
	waitErr := <-rpcDone
	select {
	case handlerErr := <-cancelRPCFinished:
		if status.Code(handlerErr) != codes.DeadlineExceeded {
			t.Fatalf("server did not observe per-inbound wait cancellation: %v", handlerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled NodeAgent RPC handler did not finish while inbound lock was held")
	}
	releaseInbound()
	if err := <-lockDone; err != nil {
		t.Fatalf("release native per-inbound lifecycle lock: %v", err)
	}
	if waitErr == nil || status.Code(waitErr) != codes.DeadlineExceeded {
		t.Fatalf("mutation lock wait error = %v, want deadline exceeded", waitErr)
	}
	lifecycleGate.mu.Lock()
	waitersAfterCancel := len(lifecycleGate.waiters)
	lifecycleGate.mu.Unlock()
	if waitersAfterCancel != 0 {
		t.Fatalf("canceled targeted mutation leaked %d lifecycle waiter(s)", waitersAfterCancel)
	}
	assertOpenVPNMutationRollback(t, server, inbound.Tag, 10, dataDir, "bob")

	add := openVPNMutationRequest("native-add-bob-11", "add", 11, bobV1)
	if response, err := rpc.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("native AddUser response=%v err=%v", response, err)
	}
	// Simulate a lost ACK by replaying exactly the same operation envelope.
	if response, err := rpc.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("native ACK-loss retry response=%v err=%v", response, err)
	}
	conflicting := openVPNMutationRequest("native-add-bob-11", "add", 11,
		openVPNRuntimeUser{UserID: 3, Username: "mallory", VPNUsername: "mallory", Password: "wrong", Status: "active"})
	if _, err := rpc.AddUser(context.Background(), conflicting); err == nil {
		t.Fatal("reused operation id with a different mutation was accepted")
	}
	bob := startOpenVPNMutationClient(t, root, ns, openvpnPath, caPath, port, "bob", "bob-native-v1")
	waitOpenVPNMutationClients(t, server, inbound.Tag, "alice", "bob")
	assertNativeOpenVPNTraffic(t, ns, tunName)
	assertNativeOpenVPNSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, tunName, tunBefore.Index, aliceClientID, "alice", "bob")
	assertNativeMutationClientTun(t, ns, alice.devName, aliceTunIndex)

	bobV2 := bobV1
	bobV2.Password = "bob-native-v2"
	update := openVPNMutationRequest("native-update-bob-12", "update", 12, bobV2)
	if response, err := rpc.UpdateUser(context.Background(), update); err != nil || !response.GetAccepted() {
		t.Fatalf("native UpdateUser response=%v err=%v", response, err)
	}
	if err := waitForOpenVPNMutationNative(func() bool { return openVPNMutationCredentialMatches(dataDir, "bob", bobV2.Password) }); err != nil {
		t.Fatalf("updated native credential not visible immediately: %v", err)
	}
	waitOpenVPNMutationClients(t, server, inbound.Tag, "alice", "bob")
	// Re-authenticate Bob with the updated credential; Alice's connection is
	// kept continuously alive and its OpenVPN client process is not restarted.
	bob.stop()
	bob = startOpenVPNMutationClient(t, root, ns, openvpnPath, caPath, port, "bob", bobV2.Password)
	waitOpenVPNMutationClients(t, server, inbound.Tag, "alice", "bob")
	assertNativeOpenVPNTraffic(t, ns, tunName)
	assertNativeOpenVPNSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, tunName, tunBefore.Index, aliceClientID, "alice", "bob")
	assertNativeMutationClientTun(t, ns, alice.devName, aliceTunIndex)

	remove := &nodev1.RemoveInboundUserRequest{OperationId: "native-remove-bob-13", InboundTag: inbound.Tag,
		Email: fmt.Sprintf("%s2:13:%s", openVPNRemoveUserPrefix, "Ym9i")}
	if response, err := rpc.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("native RemoveUser response=%v err=%v", response, err)
	}
	if err := waitForOpenVPNMutationNative(func() bool {
		clients, e := openVPNMutationManagementStatus("unix", filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName(inbound.Tag), "management.sock"))
		return e == nil && !containsOpenVPNMutationClient(clients, "bob")
	}); err != nil {
		t.Fatalf("removed native user still has a live session: %v", err)
	}
	assertOpenVPNMutationRollback(t, server, inbound.Tag, 13, dataDir, "bob")
	assertNativeOpenVPNTraffic(t, ns, tunName)
	assertNativeOpenVPNSnapshot(t, server, inbound.Tag, runtimeBefore, pidBefore, tunName, tunBefore.Index, aliceClientID, "alice")
	assertNativeMutationClientTun(t, ns, alice.devName, aliceTunIndex)
	if got := openVPNMutationAuthHash(dataDir, "bob", bobV2.Password); got != "" {
		t.Fatalf("removed native credential remains in auth file (hash %s)", got)
	}
	if got := syncConfigCalls.Load(); got != 0 {
		t.Fatalf("targeted OpenVPN mutation invoked SyncConfig %d times", got)
	}
	t.Logf("native OpenVPN targeted Add/Update/Remove preserved daemon PID %d, TUN %s index %d, and Alice session", pidBefore, tunName, tunBefore.Index)
}

func readNativeCertEnv(name string) (string, error) {
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return string(raw), nil
}

func openVPNManagementReady(tag, dataDir string) bool {
	path := filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName(tag), "management.sock")
	_, err := openVPNMutationManagementStatus("unix", path)
	return err == nil
}

type nativeMutationClient struct {
	cmd     *exec.Cmd
	devName string
	log     *os.File
}

func startOpenVPNMutationClient(t *testing.T, root, ns, openvpnPath, caPath string, port int, username, password string) *nativeMutationClient {
	t.Helper()
	name := username + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	credentials := filepath.Join(root, name+".auth")
	config := filepath.Join(root, name+".conf")
	if err := os.WriteFile(credentials, []byte(username+"\n"+password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Use a short, valid, unique Linux interface name for the real client.
	dev := "t" + fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:10]
	contents := fmt.Sprintf("client\nremote 10.250.0.1 %d\nproto udp\ndev %s\nnobind\nresolv-retry infinite\npersist-key\npersist-tun\nca %s\nremote-cert-tls server\nverb 3\nauth-user-pass %s\nauth-nocache\n", port, dev, caPath, credentials)
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(root, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, openvpnPath, "--config", config)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatalf("start native OpenVPN client %s: %v", username, err)
	}
	client := &nativeMutationClient{cmd: cmd, devName: dev, log: log}
	t.Cleanup(client.stop)
	return client
}

func (c *nativeMutationClient) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	if c.log != nil {
		_ = c.log.Close()
	}
}

func waitForOpenVPNMutationNative(predicate func() bool) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("condition did not become true before timeout")
}

func waitOpenVPNMutationClients(t *testing.T, server *Server, tag string, expected ...string) string {
	t.Helper()
	path := filepath.Join(server.cfg.DataDir, "openvpn", openVPNRuntimeDirName(tag), "management.sock")
	aliceClientID := ""
	err := waitForOpenVPNMutationNative(func() bool {
		clients, err := openVPNMutationManagementStatus("unix", path)
		if err != nil || len(clients) != len(expected) {
			return false
		}
		for _, username := range expected {
			if !containsOpenVPNMutationClient(clients, username) {
				return false
			}
			if strings.EqualFold(username, "alice") {
				for _, client := range clients {
					if strings.EqualFold(client.Username, username) || strings.EqualFold(client.CommonName, username) {
						aliceClientID = strings.TrimSpace(client.ClientID)
					}
				}
			}
		}
		return aliceClientID != ""
	})
	if err != nil {
		clients, _ := openVPNMutationManagementStatus("unix", path)
		t.Fatalf("native OpenVPN clients %v did not reach expected %v: %v", clients, expected, err)
	}
	return aliceClientID
}

func containsOpenVPNMutationClient(clients []openVPNStatusClient, username string) bool {
	for _, client := range clients {
		if strings.EqualFold(client.Username, username) || strings.EqualFold(client.CommonName, username) {
			return true
		}
	}
	return false
}

func assertNativeOpenVPNTraffic(t *testing.T, ns, tunName string) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "1", "-W", "2", "10.211.0.1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native OpenVPN tunnel traffic on %s failed: %v: %s", tunName, err, output)
	}
}

func assertNativeOpenVPNSnapshot(t *testing.T, server *Server, tag string, runtimeBefore *openVPNProcess, pidBefore int, tunName string, tunIndex int, aliceClientID string, expectedUsers ...string) {
	t.Helper()
	server.mu.Lock()
	runtimeAfter := server.openVPNRuntimes[tag]
	server.mu.Unlock()
	if runtimeAfter != runtimeBefore || runtimeAfter.cmd.Process.Pid != pidBefore {
		t.Fatalf("native OpenVPN daemon restarted: before PID=%d after=%v", pidBefore, runtimeAfter)
	}
	iface, err := net.InterfaceByName(tunName)
	if err != nil || iface.Index != tunIndex {
		t.Fatalf("native OpenVPN TUN identity changed: before index=%d after=%v err=%v", tunIndex, iface, err)
	}
	path := filepath.Join(server.cfg.DataDir, "openvpn", openVPNRuntimeDirName(tag), "management.sock")
	clients, err := openVPNMutationManagementStatus("unix", path)
	if err != nil {
		t.Fatalf("read native OpenVPN session state: %v", err)
	}
	for _, username := range expectedUsers {
		if !containsOpenVPNMutationClient(clients, username) {
			t.Fatalf("unrelated/expected native OpenVPN client %q not connected: %+v", username, clients)
		}
	}
	for _, client := range clients {
		if strings.EqualFold(client.Username, "alice") || strings.EqualFold(client.CommonName, "alice") {
			if strings.TrimSpace(client.ClientID) != aliceClientID {
				t.Fatalf("unrelated Alice session was replaced: client id before=%q after=%q", aliceClientID, client.ClientID)
			}
			return
		}
	}
	t.Fatal("unrelated Alice session is absent from native OpenVPN status")
}

func nativeMutationClientTunIndex(t *testing.T, ns, devName string) int {
	t.Helper()
	output, err := exec.Command("ip", "netns", "exec", ns, "ip", "-o", "link", "show", "dev", devName).CombinedOutput()
	if err != nil {
		t.Fatalf("read native client TUN %s: %v: %s", devName, err, output)
	}
	var index int
	if _, err := fmt.Sscanf(string(output), "%d:", &index); err != nil || index <= 0 {
		t.Fatalf("parse native client TUN index from %q: %v", output, err)
	}
	return index
}

func assertNativeMutationClientTun(t *testing.T, ns, devName string, expectedIndex int) {
	t.Helper()
	if got := nativeMutationClientTunIndex(t, ns, devName); got != expectedIndex {
		t.Fatalf("unrelated client TUN identity changed: before index=%d after=%d", expectedIndex, got)
	}
}

func assertOpenVPNMutationRollback(t *testing.T, server *Server, tag string, revision uint64, dataDir, username string) {
	t.Helper()
	current, err := server.currentRuntimePolicy()
	if err != nil || current == nil || current.Revision != revision {
		t.Fatalf("runtime policy after rollback=%+v err=%v, want revision %d", current, err, revision)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName(tag), "ccd", username)); !os.IsNotExist(err) {
		t.Fatalf("stale CCD for rolled-back/removed user remains: %v", err)
	}
	if got := openVPNMutationAuthHash(dataDir, username, "bob-native-v1"); got != "" {
		t.Fatalf("stale credential for rolled-back/removed user remains (hash %s)", got)
	}
}

func openVPNMutationAuthHash(dataDir, username, password string) string {
	path := filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName("native-mutation"), "credentials.sha256")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(username + "\x00" + password))
	needle := fmt.Sprintf("%x", sum[:])
	if strings.Contains(string(raw), needle) {
		return needle
	}
	return ""
}

func openVPNMutationCredentialMatches(dataDir, username, password string) bool {
	return openVPNMutationAuthHash(dataDir, username, password) != ""
}
