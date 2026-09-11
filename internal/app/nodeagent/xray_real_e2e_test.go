package nodeagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type realXrayLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *realXrayLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *realXrayLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRealXrayOnlineAndStatsE2E(t *testing.T) {
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
		t.Fatalf(
			"real E2E must run against pinned Xray v26.7.11; got:\n%s",
			versionOutput,
		)
	}

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start echo listener: %v", err)
	}
	defer echoListener.Close()

	echoPort := echoListener.Addr().(*net.TCPAddr).Port
	usedPorts := map[int]bool{echoPort: true}

	nextPort := func() int {
		for {
			port := realXrayFreeTCPPort(t)
			if !usedPorts[port] {
				usedPorts[port] = true
				return port
			}
		}
	}

	apiPort := nextPort()
	serverPort := nextPort()
	clientPort := nextPort()

	const (
		userEmail = "42.alice"
		userID    = "11111111-1111-4111-8111-111111111111"
	)

	serverConfig := fmt.Sprintf(`{
"log": {
"loglevel": "warning"
},
"api": {
"tag": "API",
"services": [
"HandlerService",
"StatsService",
"LoggerService"
]
},
"stats": {},
"policy": {
"levels": {
"0": {
"statsUserUplink": true,
"statsUserDownlink": true,
"statsUserOnline": true
}
}
},
"inbounds": [
{
"listen": "127.0.0.1",
"port": %d,
"protocol": "tunnel",
"settings": {
"allowedNetwork": "tcp",
"rewriteAddress": "127.0.0.1"
},
"tag": "API_INBOUND"
},
{
"listen": "127.0.0.1",
"port": %d,
"protocol": "vless",
"settings": {
"clients": [
{
"id": "%s",
"email": "%s",
"level": 0
}
],
"decryption": "none"
},
"tag": "vless-e2e"
}
],
"outbounds": [
{
"protocol": "freedom",
"settings": {
"finalRules": [
{
"action": "allow"
}
]
},
"tag": "direct"
}
],
"routing": {
"rules": [
{
"type": "field",
"inboundTag": ["API_INBOUND"],
"outboundTag": "API"
}
]
}
}`, apiPort, serverPort, userID, userEmail)

	clientConfig := fmt.Sprintf(`{
"log": {
"loglevel": "warning"
},
"inbounds": [
{
"listen": "127.0.0.1",
"port": %d,
"protocol": "dokodemo-door",
"settings": {
"address": "127.0.0.1",
"port": %d,
"network": "tcp"
},
"tag": "e2e-entry"
}
],
"outbounds": [
{
"protocol": "vless",
"sendThrough": "127.0.0.2",
"settings": {
"vnext": [
{
"address": "127.0.0.1",
"port": %d,
"users": [
{
"id": "%s",
"encryption": "none"
}
]
}
]
},
"tag": "e2e-vless"
}
]
}`, clientPort, echoPort, serverPort, userID)

	tmp := t.TempDir()
	serverConfigPath := filepath.Join(tmp, "server.json")
	clientConfigPath := filepath.Join(tmp, "client.json")

	if err := os.WriteFile(serverConfigPath, []byte(serverConfig), 0600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	if err := os.WriteFile(clientConfigPath, []byte(clientConfig), 0600); err != nil {
		t.Fatalf("write client config: %v", err)
	}

	serverLogs := realXrayStartProcess(t, xrayPath, serverConfigPath)
	realXrayWaitForTCP(t, apiPort, serverLogs)

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)

	go func() {
		conn, err := echoListener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	clientLogs := realXrayStartProcess(t, xrayPath, clientConfigPath)
	clientConn := realXrayDialUntilReady(t, clientPort, clientLogs)
	defer clientConn.Close()

	payload := []byte("antimage-real-xray-e2e")
	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("write through Xray client: %v", err)
	}

	var upstreamConn net.Conn
	select {
	case upstreamConn = <-accepted:
		defer upstreamConn.Close()
	case err := <-acceptErr:
		t.Fatalf("echo accept: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf(
			"VLESS tunnel did not reach destination\nserver:\n%s\nclient:\n%s",
			serverLogs.String(),
			clientLogs.String(),
		)
	}

	if err := upstreamConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set upstream read deadline: %v", err)
	}

	gotPayload := make([]byte, len(payload))
	if _, err := io.ReadFull(upstreamConn, gotPayload); err != nil {
		t.Fatalf("read tunneled payload: %v", err)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf(
			"tunneled payload=%q, want %q",
			gotPayload,
			payload,
		)
	}

	reply := []byte("e2e-ok")
	if _, err := upstreamConn.Write(reply); err != nil {
		t.Fatalf("write tunneled reply: %v", err)
	}

	if err := clientConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set client read deadline: %v", err)
	}

	gotReply := make([]byte, len(reply))
	if _, err := io.ReadFull(clientConn, gotReply); err != nil {
		t.Fatalf("read tunneled reply: %v", err)
	}
	if string(gotReply) != string(reply) {
		t.Fatalf("tunneled reply=%q, want %q", gotReply, reply)
	}

	onlineClient := newXrayOnlineClient(xrayPath, apiPort)

	if count := realXrayWaitForOnline(t, onlineClient, userEmail); count <= 0 {
		t.Fatalf("online count=%d, want >0", count)
	}

	realXrayWaitForBulkOnline(t, onlineClient, userEmail)

	statsClient := newXrayStatsClient(xrayPath, apiPort)
	realXrayWaitForPositiveUserTraffic(
		t,
		statsClient,
		userEmail,
	)

	// Keep the connection open but idle. Online state must come from OnlineMap,
	// not from a fresh traffic delta.
	time.Sleep(750 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	idleCount, err := onlineClient.queryOnlineCount(ctx, userEmail)
	cancel()
	if err != nil {
		t.Fatalf("query idle online user: %v", err)
	}
	if idleCount <= 0 {
		t.Fatalf("idle connected user count=%d, want >0", idleCount)
	}

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
	if err := upstreamConn.Close(); err != nil {
		t.Fatalf("close upstream connection: %v", err)
	}

	realXrayWaitForOffline(t, onlineClient, userEmail)
}

func realXrayFreeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate TCP port: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP port: %v", err)
	}

	return port
}

func realXrayStartProcess(
	t *testing.T,
	xrayPath string,
	configPath string,
) *realXrayLogBuffer {
	t.Helper()

	logs := &realXrayLogBuffer{}

	cmd := exec.Command(
		xrayPath,
		"run",
		"-config",
		configPath,
	)
	cmd.Env = append(
		os.Environ(),
		"XRAY_LOCATION_ASSET="+filepath.Dir(xrayPath),
	)
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start Xray with %s: %v", configPath, err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	return logs
}

func realXrayWaitForTCP(
	t *testing.T,
	port int,
	logs *realXrayLogBuffer,
) {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	address := net.JoinHostPort(
		"127.0.0.1",
		strconv.Itoa(port),
	)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout(
			"tcp",
			address,
			150*time.Millisecond,
		)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf(
		"Xray port %d did not become ready\n%s",
		port,
		logs.String(),
	)
}

func realXrayDialUntilReady(
	t *testing.T,
	port int,
	logs *realXrayLogBuffer,
) net.Conn {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	address := net.JoinHostPort(
		"127.0.0.1",
		strconv.Itoa(port),
	)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout(
			"tcp",
			address,
			250*time.Millisecond,
		)
		if err == nil {
			return conn
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf(
		"could not connect to Xray port %d\n%s",
		port,
		logs.String(),
	)
	return nil
}

func realXrayWaitForOnline(
	t *testing.T,
	client *xrayOnlineClient,
	email string,
) int32 {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		count, err := client.queryOnlineCount(ctx, email)
		cancel()

		if err == nil && count > 0 {
			return count
		}

		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf(
		"user %q never became online; last error=%v",
		email,
		lastErr,
	)
	return 0
}

func realXrayWaitForBulkOnline(
	t *testing.T,
	client *xrayOnlineClient,
	email string,
) {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		users, err := client.queryAllOnlineUsers(ctx)
		cancel()

		if err == nil && users[email] > 0 {
			return
		}

		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf(
		"user %q never appeared in bulk OnlineMap; last error=%v",
		email,
		lastErr,
	)
}

func realXrayWaitForPositiveUserTraffic(
	t *testing.T,
	client *xrayStatsClient,
	email string,
) {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		stats, err := client.queryStats(
			ctx,
			"user>>>"+email,
			false,
		)
		cancel()

		if err == nil {
			for _, stat := range stats {
				parsed, ok := parseXrayStatName(stat.Name)
				if !ok {
					continue
				}
				if parsed.Type == "user" &&
					parsed.Email == email &&
					parsed.Metric == "traffic" &&
					stat.Value > 0 {
					return
				}
			}
		}

		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf(
		"user %q never produced positive Xray traffic stats; last error=%v",
		email,
		lastErr,
	)
}

func realXrayWaitForOffline(
	t *testing.T,
	client *xrayOnlineClient,
	email string,
) {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error
	var lastCount int32

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		count, err := client.queryOnlineCount(ctx, email)
		cancel()

		if err == nil && count == 0 {
			return
		}

		lastCount = count
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf(
		"user %q did not become offline; count=%d last error=%v",
		email,
		lastCount,
		lastErr,
	)
}
