package nodeagent

import (
	"context"
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
	"syscall"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestIKEv2TargetedMutationNativeE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_IKEV2_TARGETED_E2E") != "1" {
		t.Skip("requires privileged native IKEv2 targeted harness")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires Linux root")
	}
	root := strings.TrimSpace(os.Getenv("ANTIMAGE_IKEV2_TARGETED_ROOT"))
	clientNS := strings.TrimSpace(os.Getenv("ANTIMAGE_IKEV2_CLIENT_NS"))
	if root == "" || clientNS == "" {
		t.Fatal("targeted root and client namespace are required")
	}
	pid, err := strconv.Atoi(os.Getenv("ANTIMAGE_IKEV2_CHARON_PID"))
	if err != nil || pid <= 0 {
		t.Fatal("invalid charon pid")
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe")); err != nil {
		t.Fatalf("charon pid unavailable: %v", err)
	}
	oldDirs := [5]string{ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir, l2TPIPSecConfigPath, l2TPIPSecSecretsPath}
	ikev2CACertDir = "/etc/ipsec.d/cacerts"
	ikev2ServerCertDir = "/etc/ipsec.d/certs"
	ikev2ServerKeyDir = "/etc/ipsec.d/private"
	l2TPIPSecConfigPath = "/etc/ipsec.conf"
	l2TPIPSecSecretsPath = "/etc/ipsec.secrets"
	t.Cleanup(func() {
		ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir, l2TPIPSecConfigPath, l2TPIPSecSecretsPath = oldDirs[0], oldDirs[1], oldDirs[2], oldDirs[3], oldDirs[4]
	})
	ca, _ := os.ReadFile(filepath.Join(root, "ca.pem"))
	cert, _ := os.ReadFile(filepath.Join(root, "server.pem"))
	key, _ := os.ReadFile(filepath.Join(root, "server.key"))
	if len(ca) == 0 || len(cert) == 0 || len(key) == 0 {
		t.Fatal("native certificate fixture missing")
	}
	alice := ikev2RuntimeUser{UserID: 1, Username: "alice", Password: "alice-native-v1", Status: "active"}
	inbound := ikev2RuntimeInbound{Tag: "native-targeted", Port: 500, Settings: map[string]any{"auth_mode": "password+certificate", "certificate_mode": "manual", "server_identity": "server", "ca_certificate": string(ca), "server_certificate": string(cert), "server_key": string(key), "ipv4_pool_cidr": "10.82.0.0/24", "ike_proposals": "aes256-sha256-modp2048!", "esp_proposals": "aes256-sha256!"}, Users: []ikev2RuntimeUser{alice}}
	originalCommand := l2TPCommandContext
	t.Cleanup(func() { l2TPCommandContext = originalCommand })
	s := New(Config{DataDir: filepath.Join(root, "targeted-node")})
	files, err := s.prepareIKEv2Inbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyIKEv2Runtimes([]preparedIKEv2Runtime{{Tag: inbound.Tag, Inbound: inbound, Files: files}}); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	var syncCalls atomic.Int32
	gs := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if info.FullMethod == nodev1.NodeRuntimeService_SyncConfig_FullMethodName {
			syncCalls.Add(1)
		}
		return h(ctx, req)
	}))
	nodev1.RegisterNodeRuntimeServiceServer(gs, s)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rpc := nodev1.NewNodeRuntimeServiceClient(conn)
	aliceBase := nativeIKEv2EstablishedSACount(t)
	aliceProc := startNativeIKEv2EAPClient(t, clientNS, "alice", "alice-native-v1", filepath.Join("/etc/ipsec.d", "client.p12"))
	defer aliceProc.stop()
	aliceSA := waitNativeIKEv2EstablishedSAs(t, aliceBase, "alice")
	assertNativeIKEv2Traffic(t, clientNS)
	pidBefore := pid
	bobV1 := ikev2RuntimeUser{UserID: 2, Username: "bob", Password: "bob-native-v1", Status: "active"}
	add := ikev2NativeMutationRequest("ike-add-bob", inbound.Tag, "add", 11, bobV1)
	if res, e := rpc.AddUser(context.Background(), add); e != nil || !res.GetAccepted() {
		t.Fatalf("AddUser response=%v err=%v", res, e)
	}
	if _, e := rpc.AddUser(context.Background(), add); e != nil {
		t.Fatalf("retry: %v", e)
	}
	conf := ikev2NativeMutationRequest("ike-add-bob", inbound.Tag, "add", 11, ikev2RuntimeUser{UserID: 3, Username: "mallory", Password: "x", Status: "active"})
	if _, e := rpc.AddUser(context.Background(), conf); e == nil {
		t.Fatal("conflicting operation id accepted")
	}
	bob := startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v1", filepath.Join("/etc/ipsec.d", "client.p12"))
	if got := waitNativeIKEv2EstablishedSAs(t, aliceSA, "bob"); got != aliceSA+1 {
		t.Fatalf("bob SA count=%d, want %d", got, aliceSA+1)
	}
	assertNativeIKEv2Traffic(t, clientNS)
	assertNativeIKEv2PID(t, pidBefore)
	bobV2 := bobV1
	bobV2.Password = "bob-native-v2"
	up := ikev2NativeMutationRequest("ike-update-bob", inbound.Tag, "update", 12, bobV2)
	if res, e := rpc.UpdateUser(context.Background(), up); e != nil || !res.GetAccepted() {
		t.Fatalf("UpdateUser response=%v err=%v", res, e)
	}
	bob.stop()
	waitNativeIKEv2EstablishedSACountAtMost(t, aliceSA, "bob v1 SA teardown")
	old := startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v1", filepath.Join("/etc/ipsec.d", "client.p12"))
	assertNativeIKEv2NoExtraSA(t, aliceSA, "bob with stale password", 3*time.Second)
	old.stop()
	bob = startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v2", filepath.Join("/etc/ipsec.d", "client.p12"))
	if got := waitNativeIKEv2EstablishedSAs(t, aliceSA, "bob after credential rotation"); got != aliceSA+1 {
		t.Fatalf("bob rotated SA count=%d, want %d", got, aliceSA+1)
	}
	assertNativeIKEv2Traffic(t, clientNS)
	assertNativeIKEv2PID(t, pidBefore)
	rem := &nodev1.RemoveInboundUserRequest{OperationId: "ike-remove-bob", InboundTag: inbound.Tag, Email: fmt.Sprintf("ikev2-user:%d:%d", bobV2.UserID, 13)}
	if res, e := rpc.RemoveUser(context.Background(), rem); e != nil || !res.GetAccepted() {
		t.Fatalf("RemoveUser response=%v err=%v", res, e)
	}
	bob.stop()
	waitNativeIKEv2EstablishedSACountAtMost(t, aliceSA, "bob v2 SA teardown")
	old = startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v2", filepath.Join("/etc/ipsec.d", "client.p12"))
	assertNativeIKEv2NoExtraSA(t, aliceSA, "removed bob", 3*time.Second)
	old.stop()
	assertNativeIKEv2PID(t, pidBefore)
	if got := waitNativeIKEv2EstablishedSACountAtMost(t, aliceSA, "alice preserved"); got != aliceSA {
		t.Fatalf("alice SA count changed from %d to %d", aliceSA, got)
	}
	assertNativeIKEv2Traffic(t, clientNS)
	if syncCalls.Load() != 0 {
		t.Fatalf("targeted path invoked SyncConfig %d times", syncCalls.Load())
	}
	stale := ikev2NativeMutationRequest("ike-stale", inbound.Tag, "add", 1, ikev2RuntimeUser{UserID: 4, Username: "stale", Password: "x", Status: "active"})
	if _, e := rpc.AddUser(context.Background(), stale); e == nil {
		t.Fatal("stale revision accepted")
	}
}

type nativeIKEv2Client struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func startNativeIKEv2EAPClient(t *testing.T, ns, user, password, p12 string) *nativeIKEv2Client {
	t.Helper()
	// The fixture PKCS#12 certificate identifies "client"; EAP identifies the user.
	baseConfig := strings.TrimSpace(os.Getenv("ANTIMAGE_IKEV2_CLIENT_STRONGSWAN_CONF"))
	basePrefix := strings.TrimSpace(os.Getenv("ANTIMAGE_IKEV2_CLIENT_CONFIG_PREFIX"))
	if baseConfig == "" || basePrefix == "" {
		t.Fatal("isolated strongSwan client configuration is required")
	}
	config, err := os.ReadFile(baseConfig)
	if err != nil {
		t.Fatalf("read isolated strongSwan client configuration: %v", err)
	}
	clientID := atomic.AddUint64(&nativeIKEv2ClientSequence, 1)
	clientPrefix := fmt.Sprintf("%s-targeted-%s-%d", basePrefix, user, clientID)
	clientConfig := filepath.Join(os.Getenv("ANTIMAGE_IKEV2_TARGETED_ROOT"), fmt.Sprintf("targeted-client-%s-%d.conf", user, clientID))
	if err := os.WriteFile(clientConfig, []byte(strings.ReplaceAll(string(config), basePrefix, clientPrefix)), 0600); err != nil {
		t.Fatalf("write isolated strongSwan client configuration: %v", err)
	}
	log, err := os.CreateTemp(os.Getenv("ANTIMAGE_IKEV2_TARGETED_ROOT"), "targeted-client-"+user+"-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, "sh", "-c", fmt.Sprintf("printf '%%s\\n' %q | exec charon-cmd --host 10.80.0.1 --identity client --eap-identity %s --remote-identity server --p12 %s --profile ikev2-pub-eap --remote-ts 10.81.0.1/32 --ike-proposal aes256-sha256-modp2048 --esp-proposal aes256-sha256", password, user, p12))
	cmd.Env = append(os.Environ(), "STRONGSWAN_CONF="+clientConfig)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	_ = log.Close()
	client := &nativeIKEv2Client{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(client.exited)
	}()
	return client
}

var nativeIKEv2ClientSequence uint64

func (c *nativeIKEv2Client) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	select {
	case <-c.exited:
		return
	default:
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.exited:
		return
	case <-time.After(5 * time.Second):
	}
	_ = c.cmd.Process.Kill()
	<-c.exited
}

func nativeIKEv2EstablishedSACount(t *testing.T) int {
	t.Helper()
	out, err := nativeIKEv2ListSAs()
	if err != nil {
		t.Fatalf("swanctl --list-sas failed: %v\n%s", err, out)
	}
	return strings.Count(string(out), "ESTABLISHED")
}

func waitNativeIKEv2EstablishedSAs(t *testing.T, baseline int, label string) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := nativeIKEv2ListSAs()
		last = string(out)
		if err != nil {
			last = fmt.Sprintf("swanctl error: %v\n%s", err, last)
		}
		if count := strings.Count(string(out), "ESTABLISHED"); err == nil && count > baseline {
			return count
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := nativeIKEv2ListSAs(); err == nil {
		if count := strings.Count(string(out), "ESTABLISHED"); count > baseline {
			return count
		}
		last = string(out)
	}
	t.Fatalf("IKEv2 established SA count for %s did not exceed %d; last swanctl output:\n%s", label, baseline, last)
	return 0
}

func waitNativeIKEv2EstablishedSACountAtMost(t *testing.T, limit int, label string) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last string
	var count int
	for time.Now().Before(deadline) {
		out, err := nativeIKEv2ListSAs()
		last = string(out)
		if err != nil {
			last = fmt.Sprintf("swanctl error: %v\n%s", err, last)
		}
		count = strings.Count(string(out), "ESTABLISHED")
		if err == nil && count <= limit {
			return count
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := nativeIKEv2ListSAs(); err == nil {
		count = strings.Count(string(out), "ESTABLISHED")
		if count <= limit {
			return count
		}
		last = string(out)
	}
	t.Fatalf("IKEv2 established SA count for %s did not drop to at most %d (got %d); last swanctl output:\n%s", label, limit, count, last)
	return count
}

func assertNativeIKEv2NoExtraSA(t *testing.T, limit int, label string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last string
	var count int
	for time.Now().Before(deadline) {
		out, err := nativeIKEv2ListSAs()
		last = string(out)
		if err == nil {
			count = strings.Count(string(out), "ESTABLISHED")
			if count <= limit {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := nativeIKEv2ListSAs(); err == nil {
		count = strings.Count(string(out), "ESTABLISHED")
		if count <= limit {
			return
		}
		last = string(out)
	}
	t.Fatalf("IKEv2 established SA count for %s exceeded %d (got %d); last swanctl output:\n%s", label, limit, count, last)
}

func nativeIKEv2ListSAs() ([]byte, error) {
	uri := strings.TrimSpace(os.Getenv("ANTIMAGE_IKEV2_SERVER_VICI_URI"))
	if uri == "" {
		uri = "unix:///run/charon.vici"
	}
	return exec.Command("swanctl", "--list-sas", "--uri", uri).CombinedOutput()
}
func assertNativeIKEv2Traffic(t *testing.T, ns string) {
	t.Helper()
	if out, err := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "1", "-W", "2", "10.81.0.1").CombinedOutput(); err != nil {
		t.Fatalf("IKEv2 traffic failed: %v: %s", err, out)
	}
}
func assertNativeIKEv2PID(t *testing.T, pid int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "exe")); err != nil {
		t.Fatalf("charon PID changed/exited: %v", err)
	}
}
func ikev2NativeMutationRequest(id, tag, op string, rev uint64, u ikev2RuntimeUser) *nodev1.InboundUserRequest {
	f := map[string]string{ikev2MutationUserIDField: strconv.FormatInt(u.UserID, 10), ikev2MutationOperation: op, ikev2MutationRevision: strconv.FormatUint(rev, 10)}
	if op != "remove" {
		raw, _ := json.Marshal(u)
		f[ikev2MutationUserJSON] = string(raw)
	}
	return &nodev1.InboundUserRequest{OperationId: id, InboundTag: tag, User: &nodev1.InboundUser{Protocol: "ikev2", Fields: f}}
}
