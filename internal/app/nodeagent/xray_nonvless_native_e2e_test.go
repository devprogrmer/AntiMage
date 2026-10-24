package nodeagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// nativeXrayProtocolCase describes one pinned-core protocol the targeted user
// path has to serve without restarting the daemon.
type nativeXrayProtocolCase struct {
	// name labels the subtest, inbound tag and client config files. It differs
	// from protocol when two pinned-core protocols share one proxies.type (both
	// Shadowsocks AEAD and Shadowsocks 2022 users report "shadowsocks").
	name       string
	protocol   string
	inboundTag string
	// seedUser / seedCredential is a second, independent user written into the
	// inbound at startup so an unrelated authenticated stream can be kept alive
	// across every mutation of the user under test.
	seedUser map[string]string
	// credential and updated are the InboundUser.Fields the controller sends.
	credential map[string]string
	updated    map[string]string
	// credentialField names the entry of those maps that carries the secret the
	// core stores under accountField, which is not always the same key (a
	// Shadowsocks 2022 user is sent as "password" but stored as "key").
	credentialField string
	// streamSettings renders the inbound stream settings. It is empty for the
	// plain-TCP protocols and carries the TLS/hysteria transport for
	// Hysteria2. It receives the fixture directory and port.
	streamSettings func(dir string, port int) string
	// serverSettings renders the inbound "settings" object.
	serverSettings func(port int) string
	// clientSettings renders the outbound "settings" object plus, for Hysteria2,
	// the matching stream settings via clientStream.
	clientSettings func(port int, credential map[string]string) string
	clientStream   func(dir string, port int, credential map[string]string) string
	accountMessage string
	accountField   string
}

func nativeXrayProtocolCases() []nativeXrayProtocolCase {
	return []nativeXrayProtocolCase{
		{
			name:            "vmess",
			protocol:        "vmess",
			inboundTag:      "vmess-e2e",
			seedUser:        map[string]string{"id": "33333333-3333-4333-8333-333333333333"},
			credential:      map[string]string{"id": "11111111-1111-4111-8111-111111111111"},
			updated:         map[string]string{"id": "22222222-2222-4222-8222-222222222222"},
			credentialField: "id",
			accountMessage:  "VmessAccount",
			accountField:    "id",
			serverSettings: func(port int) string {
				return fmt.Sprintf(`"settings": {"clients": [
 {"id": "%s", "email": "8.native-vmess-unrelated", "level": 0}
]}`, "33333333-3333-4333-8333-333333333333")
			},
			clientSettings: func(port int, credential map[string]string) string {
				return fmt.Sprintf(`"settings": {"vnext": [{"address": "127.0.0.1", "port": %d, "users": [{"id": "%s", "security": "auto", "level": 0}]}]}`, port, credential["id"])
			},
		},
		{
			name:            "trojan",
			protocol:        "trojan",
			inboundTag:      "trojan-e2e",
			seedUser:        map[string]string{"password": "trojan-unrelated-secret"},
			credential:      map[string]string{"password": "trojan-secret-v1"},
			updated:         map[string]string{"password": "trojan-secret-v2"},
			credentialField: "password",
			accountMessage:  "TrojanAccount",
			accountField:    "password",
			serverSettings: func(port int) string {
				return `"settings": {"clients": [
 {"password": "trojan-unrelated-secret", "email": "8.native-trojan-unrelated", "level": 0}
]}`
			},
			clientSettings: func(port int, credential map[string]string) string {
				return fmt.Sprintf(`"settings": {"servers": [{"address": "127.0.0.1", "port": %d, "password": "%s"}]}`, port, credential["password"])
			},
		},
		{
			name:            "shadowsocks",
			protocol:        "shadowsocks",
			inboundTag:      "shadowsocks-e2e",
			seedUser:        map[string]string{"password": "ss-unrelated-secret", "method": "aes-256-gcm"},
			credential:      map[string]string{"password": "ss-secret-v1", "method": "aes-256-gcm"},
			updated:         map[string]string{"password": "ss-secret-v2", "method": "aes-256-gcm"},
			credentialField: "password",
			accountMessage:  "ShadowsocksAccount",
			accountField:    "password",
			serverSettings: func(port int) string {
				return `"settings": {"method": "aes-256-gcm", "clients": [
 {"password": "ss-unrelated-secret", "method": "aes-256-gcm", "email": "8.native-shadowsocks-unrelated", "level": 0}
]}`
			},
			clientSettings: func(port int, credential map[string]string) string {
				return fmt.Sprintf(`"settings": {"servers": [{"address": "127.0.0.1", "port": %d, "method": "aes-256-gcm", "password": "%s"}]}`, port, credential["password"])
			},
		},
		{
			// Shadowsocks 2022 multi-user shares proxies.type "shadowsocks"
			// with the AEAD path. The controller strips "method" for the 2022
			// methods (RuntimeShadowsocksSettings), which is what selects
			// xray.proxy.shadowsocks_2022.Account, and the inbound's own base64
			// server key is what the client presents.
			name:            "shadowsocks2022",
			protocol:        "shadowsocks",
			inboundTag:      "shadowsocks2022-e2e",
			seedUser:        map[string]string{"password": "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="},
			credential:      map[string]string{"password": "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI="},
			updated:         map[string]string{"password": "AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="},
			credentialField: "password",
			accountMessage:  "Shadowsocks2022Account",
			accountField:    "key",
			serverSettings: func(port int) string {
				return `"settings": {"method": "2022-blake3-aes-256-gcm", "password": "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", "clients": [
 {"password": "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", "email": "8.native-shadowsocks2022-unrelated", "level": 0}
]}`
			},
			clientSettings: func(port int, credential map[string]string) string {
				return fmt.Sprintf(`"settings": {"servers": [{"address": "127.0.0.1", "port": %d, "method": "2022-blake3-aes-256-gcm", "password": "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=:%s"}]}`, port, credential["password"])
			},
		},
		{
			// Hysteria2 authenticates with a per-user "auth" string through the
			// hysteria transport's account validator. The inbound requires a TLS
			// transport carrying a certificate, which the harness generates.
			name:            "hysteria2",
			protocol:        "hysteria",
			inboundTag:      "hysteria2-e2e",
			seedUser:        map[string]string{"auth": "hysteria-unrelated-auth"},
			credential:      map[string]string{"auth": "hysteria-secret-v1"},
			updated:         map[string]string{"auth": "hysteria-secret-v2"},
			credentialField: "auth",
			accountMessage:  "HysteriaAccount",
			accountField:    "auth",
			serverSettings: func(port int) string {
				return `"settings": {"version": 2, "clients": [
 {"auth": "hysteria-unrelated-auth", "email": "8.native-hysteria2-unrelated", "level": 0}
]}`
			},
			streamSettings: func(dir string, port int) string {
				return fmt.Sprintf(`"streamSettings": {"network": "hysteria", "security": "tls",
  "tlsSettings": {"certificates": [{"certificateFile": %q, "keyFile": %q}]},
  "hysteriaSettings": {"version": 2}}`, filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
			},
			clientSettings: func(port int, credential map[string]string) string {
				return fmt.Sprintf(`"settings": {"version": 2, "address": "127.0.0.1", "port": %d}`, port)
			},
			clientStream: func(dir string, port int, credential map[string]string) string {
				return fmt.Sprintf(`"streamSettings": {"network": "hysteria", "security": "tls",
  "tlsSettings": {"verifyPeerCertByName": "localhost", "pinnedPeerCertSha256": %q},
  "hysteriaSettings": {"version": 2, "auth": %q}}`, localhostCertFingerprint(dir), credential["auth"])
			},
		},
	}
}

// localhostCertFingerprint returns the SHA-256 fingerprint the harness renders
// into Hysteria2 client TLS settings. The pinned core removed "allowInsecure",
// so the client pins the fixture certificate instead of trusting the system
// store. It is called after the certificate exists.
func localhostCertFingerprint(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// writeNativeXrayCertificate writes a throwaway localhost certificate plus key
// into dir for the TLS-based protocols in this harness.
func writeNativeXrayCertificate(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate hysteria certificate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create hysteria certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal hysteria certificate key: %v", err)
	}
	writePEM := func(name, blockType string, bytes []byte) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: bytes}), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writePEM("cert.pem", "CERTIFICATE", der)
	writePEM("key.pem", "EC PRIVATE KEY", keyDER)
}

// TestXrayNonVLESSNativeTargetedMutationE2E drives real AddUser/UpdateUser/
// RemoveUser calls through the NodeAgent gRPC handlers against a live pinned
// Xray core for every non-VLESS protocol the pinned core serves as an inbound.
//
// It asserts the properties the capability advertisement depends on:
//   - the daemon PID never changes (no restart, no full sync)
//   - an unrelated authenticated user keeps transferring traffic across every
//     mutation, so a whole-process restart or a cross-user disconnect is caught
//   - the added credential authenticates immediately
//   - the update takes effect and the previous credential stops working
//   - removal prevents re-authentication
//   - a retry of the same underlying user state is idempotent
//   - a divergent credential under the same email is refused, not silently applied
func TestXrayNonVLESSNativeTargetedMutationE2E(t *testing.T) {
	xrayPath := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_E2E_BINARY"))
	if xrayPath == "" {
		t.Skip("set ANTIMAGE_XRAY_E2E_BINARY to run the real Xray E2E test")
	}
	if _, err := os.Stat(xrayPath); err != nil {
		t.Fatalf("Xray E2E binary: %v", err)
	}
	versionOutput, err := exec.Command(xrayPath, "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("read Xray version: %v\n%s", err, versionOutput)
	}
	if !strings.Contains(string(versionOutput), "26.7.11") {
		t.Fatalf("real E2E must run against pinned Xray v26.7.11; got:\n%s", versionOutput)
	}

	for _, c := range nativeXrayProtocolCases() {
		t.Run(c.name, func(t *testing.T) {
			runNativeXrayProtocolMutation(t, xrayPath, c)
		})
	}
}

func runNativeXrayProtocolMutation(t *testing.T, xrayPath string, c nativeXrayProtocolCase) {
	t.Helper()

	usedPorts := map[int]bool{}
	nextPort := func() int {
		for {
			port := realXrayFreeTCPPort(t)
			if !usedPorts[port] {
				usedPorts[port] = true
				return port
			}
		}
	}
	fixtureDir := t.TempDir()
	writeNativeXrayCertificate(t, fixtureDir)
	stream := ""
	if c.streamSettings != nil {
		stream = ", " + c.streamSettings(fixtureDir, 0)
	}
	apiPort := nextPort()
	serverPort := nextPort()
	serverConfig := fmt.Sprintf(`{
 "log": {"loglevel": "warning"},
 "api": {"tag": "API", "services": ["HandlerService", "StatsService", "LoggerService"]},
 "stats": {},
 "policy": {"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true}}},
 "inbounds": [
 {"listen": "127.0.0.1", "port": %d, "protocol": "tunnel",
  "settings": {"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1"}, "tag": "API_INBOUND"},
 {"listen": "127.0.0.1", "port": %d, "protocol": "%s", %s%s, "tag": "%s"}
 ],
 "outbounds": [{"protocol": "freedom", "settings": {"finalRules": [{"action": "allow"}]}, "tag": "direct"}],
 "routing": {"rules": [{"type": "field", "inboundTag": ["API_INBOUND"], "outboundTag": "API"}]}
 }`, apiPort, serverPort, c.protocol, c.serverSettings(serverPort), stream, c.inboundTag)

	serverConfigPath := filepath.Join(fixtureDir, c.name+"-server.json")
	if err := os.WriteFile(serverConfigPath, []byte(serverConfig), 0600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	serverProcess := realXrayStartProcess(t, xrayPath, serverConfigPath)
	realXrayWaitForTCP(t, apiPort, serverProcess.logs)
	pidBefore := serverProcess.cmd.Process.Pid

	agent := New(Config{XrayPath: xrayPath, XrayAPIPort: apiPort})
	t.Cleanup(agent.closeXrayStatsClient)
	statsClient := agent.cachedXrayStatsClient(xrayPath, apiPort)

	email := "7.native-" + c.name + "-user"
	unrelated := newNativeXrayTunnel(t, xrayPath, &usedPorts, c, fixtureDir, c.seedUser, serverPort, c.name+"-unrelated")
	defer unrelated.close()

	addUser := func(operationID string, credential map[string]string, wantCode codes.Code) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := agent.AddUser(ctx, &nodev1.InboundUserRequest{
			OperationId: operationID,
			InboundTag:  c.inboundTag,
			User:        &nodev1.InboundUser{Protocol: c.protocol, Email: email, Fields: credential},
		})
		if wantCode != codes.OK {
			if status.Code(err) != wantCode {
				t.Fatalf("AddUser(%s) error=%v, want code %v", operationID, err, wantCode)
			}
			return err
		}
		if err != nil || !response.GetAccepted() {
			t.Fatalf("AddUser(%s) response=%v err=%v", operationID, response, err)
		}
		return nil
	}
	updateUser := func(operationID string, credential map[string]string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := agent.UpdateUser(ctx, &nodev1.InboundUserRequest{
			OperationId: operationID,
			InboundTag:  c.inboundTag,
			User:        &nodev1.InboundUser{Protocol: c.protocol, Email: email, Fields: credential},
		})
		if err != nil || !response.GetAccepted() {
			t.Fatalf("UpdateUser(%s) response=%v err=%v", operationID, response, err)
		}
	}
	removeUser := func(operationID string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := agent.RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
			OperationId: operationID,
			InboundTag:  c.inboundTag,
			Email:       email,
		})
		if err != nil || !response.GetAccepted() {
			t.Fatalf("RemoveUser(%s) response=%v err=%v", operationID, response, err)
		}
	}

	// Add, and prove the credential authenticates immediately.
	addUser("native-"+c.name+"-add", c.credential, codes.OK)
	assertNativeXrayStoredCredential(t, statsClient, c, email, c.credential[c.credentialField])
	newNativeXrayTunnel(t, xrayPath, &usedPorts, c, fixtureDir, c.credential, serverPort, c.name+"-added").assertAlive(t)

	// Same operation ID retried: the credential must be unchanged, and an
	// identical AddUser must not error (the core raises "already exists").
	unrelated.assertAlive(t)
	addUser("native-"+c.name+"-add", c.credential, codes.OK)
	assertNativeXrayStoredCredential(t, statsClient, c, email, c.credential[c.credentialField])

	// A different credential under the same email must be refused rather than
	// silently replacing the live one.
	conflicting := map[string]string{}
	for key, value := range c.credential {
		conflicting[key] = value
	}
	conflicting[c.credentialField] = "conflicting-" + c.name + "-credential"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conflictResponse, conflictErr := agent.AddUser(ctx, &nodev1.InboundUserRequest{
		OperationId: "native-" + c.name + "-conflict",
		InboundTag:  c.inboundTag,
		User:        &nodev1.InboundUser{Protocol: c.protocol, Email: email, Fields: conflicting},
	})
	cancel()
	if conflictErr == nil || conflictResponse != nil {
		t.Fatalf("conflicting AddUser must fail: response=%v err=%v", conflictResponse, conflictErr)
	}
	assertNativeXrayStoredCredential(t, statsClient, c, email, c.credential[c.credentialField])

	// Update must take effect without a restart, and the old credential must
	// stop authenticating.
	updateUser("native-"+c.name+"-update", c.updated)
	assertNativeXrayStoredCredential(t, statsClient, c, email, c.updated[c.credentialField])
	newNativeXrayTunnel(t, xrayPath, &usedPorts, c, fixtureDir, c.updated, serverPort, c.name+"-updated").assertAlive(t)
	assertNativeXrayCredentialRejected(t, xrayPath, &usedPorts, c, fixtureDir, c.credential, serverPort, serverProcess.logs)

	// The unrelated authenticated user must have survived Add/Update.
	unrelated.assertAlive(t)
	if got := serverProcess.cmd.Process.Pid; got != pidBefore {
		t.Fatalf("targeted %s mutations restarted the daemon: pid %d -> %d", c.name, pidBefore, got)
	}

	// Removal must prevent re-authentication, and repeating it must be safe.
	removeUser("native-" + c.name + "-remove")
	assertNativeXrayCredentialRejected(t, xrayPath, &usedPorts, c, fixtureDir, c.updated, serverPort, serverProcess.logs)
	removeUser("native-" + c.name + "-remove")

	unrelated.assertAlive(t)
	if got := serverProcess.cmd.Process.Pid; got != pidBefore {
		t.Fatalf("targeted %s removal restarted the daemon: pid %d -> %d", c.name, pidBefore, got)
	}
}

// nativeXrayEcho is a single listener that accepts and reports new
// connections, shared by the user under test and the unrelated user. Each
// caller waits for its own accepted connection.
type nativeXrayEcho struct {
	listener net.Listener
	port     int
}

func newNativeXrayEcho(t *testing.T, usedPorts *map[int]bool) *nativeXrayEcho {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start echo listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	echo := &nativeXrayEcho{
		listener: listener,
		port:     listener.Addr().(*net.TCPAddr).Port,
	}
	(*usedPorts)[echo.port] = true
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go echoNativeXrayConn(conn)
		}
	}()
	return echo
}

// echoNativeXrayConn echoes every byte back to the sender so a tunnel can prove
// a full round trip. Each connection is independent, which keeps concurrent
// tunnels from being confused with each other.
func echoNativeXrayConn(conn net.Conn) {
	defer conn.Close()
	buffer := make([]byte, 32*1024)
	for {
		read, err := conn.Read(buffer)
		if read > 0 {
			if _, writeErr := conn.Write(buffer[:read]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

type nativeXrayTunnel struct {
	conn net.Conn
}

func (n *nativeXrayTunnel) close() {
	if n == nil {
		return
	}
	if n.conn != nil {
		_ = n.conn.Close()
	}
}

// assertAlive pushes another payload through a live tunnel to prove it still
// carries traffic.
func (n *nativeXrayTunnel) assertAlive(t *testing.T) {
	t.Helper()
	payload := []byte("alive-" + fmt.Sprint(time.Now().UnixNano()))
	if _, err := n.conn.Write(payload); err != nil {
		t.Fatalf("write to live tunnel: %v", err)
	}
	if err := n.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(n.conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("live tunnel payload=%q err=%v", got, err)
	}
}

// newNativeXrayTunnel starts a client, waits for the tunnel to reach the echo
// listener, and returns the established stream. The echo listener is created
// per tunnel so concurrent tunnels cannot be mistaken for one another.
func newNativeXrayTunnel(
	t *testing.T,
	xrayPath string,
	usedPorts *map[int]bool,
	c nativeXrayProtocolCase,
	fixtureDir string,
	credential map[string]string,
	serverPort int,
	label string,
) *nativeXrayTunnel {
	t.Helper()
	echo := newNativeXrayEcho(t, usedPorts)
	clientPort := realXrayFreeTCPPort(t)
	(*usedPorts)[clientPort] = true
	stream := " "
	if c.clientStream != nil {
		stream = " " + c.clientStream(fixtureDir, serverPort, credential) + ","
	}
	config := fmt.Sprintf(`{
 "log": {"loglevel": "warning"},
 "inbounds": [{"listen": "127.0.0.1", "port": %d, "protocol": "dokodemo-door",
  "settings": {"address": "127.0.0.1", "port": %d, "network": "tcp"}, "tag": "e2e-entry"}],
 "outbounds": [{"protocol": "%s", %s,%s "tag": "e2e-%s"}]
 }`, clientPort, echo.port, c.protocol, c.clientSettings(serverPort, credential), stream, c.protocol)
	path := filepath.Join(fixtureDir, label+".json")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatalf("write %s client config: %v", label, err)
	}
	process := realXrayStartProcess(t, xrayPath, path)
	realXrayWaitForTCP(t, clientPort, process.logs)
	conn := realXrayDialUntilReady(t, clientPort, process.logs)
	probe := []byte("probe-" + label)
	if _, err := conn.Write(probe); err != nil {
		_ = conn.Close()
		t.Fatalf("%s client write: %v", label, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(probe))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, probe) {
		_ = conn.Close()
		t.Fatalf("%s probe round-trip=%q err=%v\nserver-side client:\n%s", label, got, err, process.logs.String())
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	tunnel := &nativeXrayTunnel{conn: conn}
	t.Cleanup(tunnel.close)
	return tunnel
}

// assertNativeXrayCredentialRejected requires that a wrong or removed
// credential never reaches the echo listener.
func assertNativeXrayCredentialRejected(
	t *testing.T,
	xrayPath string,
	usedPorts *map[int]bool,
	c nativeXrayProtocolCase,
	fixtureDir string,
	credential map[string]string,
	serverPort int,
	serverLogs *realXrayLogBuffer,
) {
	t.Helper()
	clientPort := realXrayFreeTCPPort(t)
	(*usedPorts)[clientPort] = true
	// Point the client at a fresh listener that records any connection, so an
	// accepted connection is unambiguous evidence the credential still worked.
	trap, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start rejection listener: %v", err)
	}
	defer trap.Close()
	reached := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := trap.Accept()
		if acceptErr != nil {
			return
		}
		reached <- conn
	}()

	stream := " "
	if c.clientStream != nil {
		stream = " " + c.clientStream(fixtureDir, serverPort, credential) + ","
	}
	config := fmt.Sprintf(`{
 "log": {"loglevel": "warning"},
 "inbounds": [{"listen": "127.0.0.1", "port": %d, "protocol": "dokodemo-door",
  "settings": {"address": "127.0.0.1", "port": %d, "network": "tcp"}, "tag": "e2e-entry"}],
 "outbounds": [{"protocol": "%s", %s,%s "tag": "e2e-%s"}]
 }`, clientPort, trap.Addr().(*net.TCPAddr).Port, c.protocol, c.clientSettings(serverPort, credential), stream, c.protocol)
	path := filepath.Join(fixtureDir, "rejected-"+c.name+".json")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatalf("write rejected client config: %v", err)
	}
	process := realXrayStartProcess(t, xrayPath, path)
	realXrayWaitForTCP(t, clientPort, process.logs)
	conn := realXrayDialUntilReady(t, clientPort, process.logs)
	defer conn.Close()
	if _, err := conn.Write([]byte("must-not-reach-destination")); err != nil {
		// A client that cannot even write is a valid rejection shape.
		return
	}
	select {
	case upstream := <-reached:
		_ = upstream.Close()
		t.Fatalf("%s credential still authenticated after update/removal\nserver:\n%s", c.protocol, serverLogs.String())
	case <-time.After(3 * time.Second):
		// Expected: the destination was never reached.
	}
}

func assertNativeXrayStoredCredential(
	t *testing.T,
	client *xrayStatsClient,
	c nativeXrayProtocolCase,
	email, want string,
) {
	t.Helper()
	user, err := client.getInboundUserRPC(context.Background(), c.inboundTag, email)
	if err != nil {
		t.Fatalf("query stored %s user: %v", c.protocol, err)
	}
	if user == nil {
		t.Fatalf("%s user %q is absent from the core", c.protocol, email)
	}
	accountTyped := user.Get(user.Descriptor().Fields().ByNumber(3)).Message()
	accountBytes := accountTyped.Get(accountTyped.Descriptor().Fields().ByNumber(2)).Bytes()
	account := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName(protoreflect.Name(c.accountMessage)))
	if err := proto.Unmarshal(accountBytes, account); err != nil {
		t.Fatalf("decode %s account: %v", c.accountMessage, err)
	}
	got := account.Get(account.Descriptor().Fields().ByName(protoreflect.Name(c.accountField))).String()
	if got != want {
		t.Fatalf("stored %s credential = %q, want %q", c.protocol, got, want)
	}
}
