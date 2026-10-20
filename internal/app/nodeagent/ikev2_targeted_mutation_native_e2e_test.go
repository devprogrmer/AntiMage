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
	aliceProc := startNativeIKEv2EAPClient(t, clientNS, "alice", "alice-native-v1", filepath.Join("/etc/ipsec.d", "client.p12"))
	defer aliceProc.stop()
	waitNativeIKEv2SA(t, "alice")
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
	waitNativeIKEv2SA(t, "bob")
	assertNativeIKEv2Traffic(t, clientNS)
	assertNativeIKEv2PID(t, pidBefore)
	bobV2 := bobV1
	bobV2.Password = "bob-native-v2"
	up := ikev2NativeMutationRequest("ike-update-bob", inbound.Tag, "update", 12, bobV2)
	if res, e := rpc.UpdateUser(context.Background(), up); e != nil || !res.GetAccepted() {
		t.Fatalf("UpdateUser response=%v err=%v", res, e)
	}
	bob.stop()
	old := startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v1", filepath.Join("/etc/ipsec.d", "client.p12"))
	assertNativeIKEv2NoSA(t, "bob", 3*time.Second)
	old.stop()
	bob = startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v2", filepath.Join("/etc/ipsec.d", "client.p12"))
	waitNativeIKEv2SA(t, "bob")
	assertNativeIKEv2Traffic(t, clientNS)
	assertNativeIKEv2PID(t, pidBefore)
	rem := &nodev1.RemoveInboundUserRequest{OperationId: "ike-remove-bob", InboundTag: inbound.Tag, Email: fmt.Sprintf("ikev2-user:%d:%d", bobV2.UserID, 13)}
	if res, e := rpc.RemoveUser(context.Background(), rem); e != nil || !res.GetAccepted() {
		t.Fatalf("RemoveUser response=%v err=%v", res, e)
	}
	bob.stop()
	old = startNativeIKEv2EAPClient(t, clientNS, "bob", "bob-native-v2", filepath.Join("/etc/ipsec.d", "client.p12"))
	assertNativeIKEv2NoSA(t, "bob", 3*time.Second)
	old.stop()
	assertNativeIKEv2PID(t, pidBefore)
	waitNativeIKEv2SA(t, "alice")
	assertNativeIKEv2Traffic(t, clientNS)
	if syncCalls.Load() != 0 {
		t.Fatalf("targeted path invoked SyncConfig %d times", syncCalls.Load())
	}
	stale := ikev2NativeMutationRequest("ike-stale", inbound.Tag, "add", 1, ikev2RuntimeUser{UserID: 4, Username: "stale", Password: "x", Status: "active"})
	if _, e := rpc.AddUser(context.Background(), stale); e == nil {
		t.Fatal("stale revision accepted")
	}
}

type nativeIKEv2Client struct{ cmd *exec.Cmd }

func startNativeIKEv2EAPClient(t *testing.T, ns, user, password, p12 string) *nativeIKEv2Client {
	t.Helper()
	// The fixture PKCS#12 certificate identifies "client"; EAP identifies the user.
	log, err := os.CreateTemp(os.Getenv("ANTIMAGE_IKEV2_TARGETED_ROOT"), "targeted-client-"+user+"-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, "sh", "-c", fmt.Sprintf("printf '%%s\\n' %q | exec charon-cmd --host 10.80.0.1 --identity client --eap-identity %s --remote-identity server --p12 %s --profile ikev2-pub-eap --remote-ts 10.81.0.1/32 --ike-proposal aes256-sha256-modp2048 --esp-proposal aes256-sha256", password, user, p12))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	_ = log.Close()
	return &nativeIKEv2Client{cmd: cmd}
}
func (c *nativeIKEv2Client) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
}
func assertNativeIKEv2NoSA(t *testing.T, user string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("swanctl", "--list-sas", "--uri", "unix:///run/charon.vici").CombinedOutput()
		if !strings.Contains(string(out), user) || !strings.Contains(string(out), "ESTABLISHED") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("IKEv2 SA for %s unexpectedly established", user)
}

func waitNativeIKEv2SA(t *testing.T, user string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var lastOutput string
	for time.Now().Before(deadline) {
		out, _ := exec.Command("swanctl", "--list-sas", "--uri", "unix:///run/charon.vici").CombinedOutput()
		lastOutput = string(out)
		if strings.Contains(lastOutput, user) && strings.Contains(lastOutput, "ESTABLISHED") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("swanctl --list-sas output at timeout: %s", lastOutput)
	t.Fatalf("IKEv2 SA for %s not established", user)
}
func assertNativeIKEv2Traffic(t *testing.T, ns string) {
	t.Helper()
	if out, err := exec.Command("ip", "netns", "exec", ns, "ping", "-c", "3", "-i", "1", "-W", "2", "10.81.0.1").CombinedOutput(); err != nil {
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
