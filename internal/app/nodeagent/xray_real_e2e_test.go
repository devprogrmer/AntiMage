package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
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

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
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
		userEmail = "7.rb1_bmF0aXZl.native-user"
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

	serverProcess := realXrayStartProcess(t, xrayPath, serverConfigPath)
	realXrayWaitForTCP(t, apiPort, serverProcess.logs)

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

	clientProcess := realXrayStartProcess(t, xrayPath, clientConfigPath)
	realXrayWaitForTCP(t, clientPort, clientProcess.logs)
	clientConn := realXrayDialUntilReady(t, clientPort, clientProcess.logs)
	defer func() {
		if clientConn != nil {
			_ = clientConn.Close()
		}
	}()

	payload := []byte("antimage-real-xray-e2e")
	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("write through Xray client: %v", err)
	}

	var upstreamConn net.Conn
	select {
	case upstreamConn = <-accepted:
		defer func() {
			if upstreamConn != nil {
				_ = upstreamConn.Close()
			}
		}()
	case err := <-acceptErr:
		t.Fatalf("echo accept: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf(
			"VLESS tunnel did not reach destination\nserver:\n%s\nclient:\n%s",
			serverProcess.logs.String(),
			clientProcess.logs.String(),
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

	bulkState := realXrayWaitForBulkOnline(t, onlineClient, userEmail)
	foundClientIP := false
	for _, item := range bulkState.IPs {
		if item.IP == "127.0.0.2" {
			foundClientIP = true
			if item.LastSeen <= 0 {
				t.Fatalf("bulk OnlineMap lastSeen=%d, want >0", item.LastSeen)
			}
		}
	}
	if !foundClientIP {
		t.Fatalf("bulk OnlineMap IPs=%+v, want client IP 127.0.0.2", bulkState.IPs)
	}

	statsClient := newXrayStatsClient(xrayPath, apiPort)
	panelTestBinary := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_PANEL_TEST_BINARY"))
	if panelTestBinary != "" {
		nativeState := filepath.Join(tmp, "xray-agent-state")
		generationA := realXrayRuntimeGeneration(serverProcess)
		batchA := runNativeXrayAccountingStage(t, "collect-first", nativeState, xrayPath, apiPort, generationA, "")
		if batchA.GetBatchId() == "" || nativeXrayBatchValue(batchA) == 0 {
			t.Fatalf("production Xray collector did not create positive native batch A: batch=%v", batchA)
		}
		runNativeXrayPanelDB(t, panelTestBinary, nativeState, false)

		// A separate test process models a real node-agent restart while the panel
		// ACK is lost. It must load and replay the immutable pending batch.
		replayA := runNativeXrayAccountingStage(t, "collect-first", nativeState, xrayPath, apiPort, generationA, "")
		if !proto.Equal(replayA, batchA) {
			t.Fatalf("Xray batch A changed across node process restart: replay=%v want=%v", replayA, batchA)
		}
		runNativeXrayPanelDB(t, panelTestBinary, nativeState, false)
		assertNativeXrayPanelReceipt(t, nativeState, batchA.GetBatchId(), nativeXrayBatchValue(batchA))
		runNativeXrayAccountingStage(t, "ack", nativeState, xrayPath, apiPort, generationA, batchA.GetBatchId())

		// Restart the actual native Xray daemon after the first batch was ACKed.
		// Reinitialize node accounting in a separate process and checkpoint the
		// new native generation before reconnecting the VLESS client.
		serverProcess.stop()
		_ = upstreamConn.Close()
		_ = clientConn.Close()
		serverProcess = realXrayStartProcess(t, xrayPath, serverConfigPath)
		realXrayWaitForTCP(t, apiPort, serverProcess.logs)
		generationB := realXrayRuntimeGeneration(serverProcess)
		runNativeXrayAccountingStage(t, "checkpoint", nativeState, xrayPath, apiPort, generationB, "")
		statsClient.rpc.close()
		statsClient = newXrayStatsClient(xrayPath, apiPort)
		onlineClient = newXrayOnlineClient(xrayPath, apiPort)

		acceptedAfterRestart := make(chan net.Conn, 1)
		acceptAfterRestartErr := make(chan error, 1)
		go func() {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				acceptAfterRestartErr <- acceptErr
				return
			}
			acceptedAfterRestart <- conn
		}()
		clientConn = realXrayDialUntilReady(t, clientPort, clientProcess.logs)
		select {
		case upstreamConn = <-acceptedAfterRestart:
		case acceptErr := <-acceptAfterRestartErr:
			t.Fatalf("accept destination after Xray runtime restart: %v", acceptErr)
		case <-time.After(5 * time.Second):
			t.Fatalf("reconnected VLESS stream did not reach destination after runtime restart\nserver:\n%s\nclient:\n%s", serverProcess.logs.String(), clientProcess.logs.String())
		}

		beforeBatchB, err := statsClient.queryStats(context.Background(), "user>>>", false)
		if err != nil {
			t.Fatalf("read Xray counters before batch B traffic: %v", err)
		}
		beforeBatchBBytes := nativeXrayUserCounterTotal(beforeBatchB, userEmail)
		continued := bytes.Repeat([]byte("xray-native-exact-once-"), 32*1024)
		if _, err := clientConn.Write(continued); err != nil {
			t.Fatalf("write batch B through Xray: %v", err)
		}
		if err := upstreamConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		gotBatchB := make([]byte, len(continued))
		if _, err := io.ReadFull(upstreamConn, gotBatchB); err != nil {
			t.Fatalf("read batch B through Xray: %v", err)
		}
		if !bytes.Equal(gotBatchB, continued) {
			t.Fatal("Xray batch B tunnel payload did not round-trip")
		}
		realXrayWaitForPositiveUserTraffic(t, statsClient, userEmail)
		realXrayWaitForNativeCounterIncrease(t, statsClient, userEmail, beforeBatchBBytes)

		// A fresh node-agent process collects after the native runtime restart.
		// SQLite reapplies A and B after reopening; totals increase by B once.
		batchB := runNativeXrayAccountingStage(t, "collect-next", nativeState, xrayPath, apiPort, generationB, "")
		if batchB.GetBatchId() == "" || batchB.GetBatchId() == batchA.GetBatchId() || nativeXrayBatchValue(batchB) == 0 {
			t.Fatalf("production Xray collector did not create positive post-restart batch B: batch=%v", batchB)
		}
		runNativeXrayPanelDB(t, panelTestBinary, nativeState, false)
		assertNativeXrayPanelReceipt(t, nativeState, batchB.GetBatchId(), nativeXrayBatchValue(batchA)+nativeXrayBatchValue(batchB))
		runNativeXrayAccountingStage(t, "ack", nativeState, xrayPath, apiPort, generationB, batchB.GetBatchId())
	}

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

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	err = statsClient.removeUserRPC(ctx, "vless-e2e", userEmail)
	cancel()
	if err != nil {
		t.Fatalf("remove real Xray user: %v", err)
	}
	// Removing authentication does not terminate an already authenticated stream.
	continuedAfterRemoval := []byte("existing-stream-after-user-removal")
	if err := upstreamConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write(continuedAfterRemoval); err != nil {
		t.Fatalf("write existing stream after user removal: %v", err)
	}
	gotContinued := make([]byte, len(continuedAfterRemoval))
	if _, err := io.ReadFull(upstreamConn, gotContinued); err != nil {
		t.Fatalf("read existing stream after user removal: %v", err)
	}
	if !bytes.Equal(gotContinued, continuedAfterRemoval) {
		t.Fatalf("existing stream payload=%q, want %q", gotContinued, continuedAfterRemoval)
	}
	t.Log("native user removal succeeded, but the existing VLESS stream still transfers traffic; hard per-user quota requires a stream termination hook")

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
	if err := upstreamConn.Close(); err != nil {
		t.Fatalf("close upstream connection: %v", err)
	}

	realXrayWaitForOffline(t, onlineClient, userEmail)
}

func newNativeXrayAccountingServer(t *testing.T, dataDir, xrayPath string, apiPort int, generation string) *Server {
	t.Helper()
	s := New(Config{DataDir: dataDir, XrayPath: xrayPath, XrayAPIPort: apiPort})
	s.mu.Lock()
	s.lastRuntime = &exec.Cmd{} // The pinned native Xray process is owned by this E2E harness.
	s.xrayRuntimeGeneration = generation
	s.mu.Unlock()
	t.Cleanup(s.closeXrayStatsClient)
	return s
}

// TestXrayNativeAccountingStage is re-executed in a new test process to verify
// durable node accounting state across actual process boundaries while the
// pinned native Xray daemon remains independently managed by the E2E harness.
func TestXrayNativeAccountingStage(t *testing.T) {
	action := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_ACTION"))
	if action == "" {
		t.Skip("requires the real Xray E2E subprocess harness")
	}
	dataDir := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_STATE"))
	xrayPath := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_PATH"))
	generation := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_GENERATION"))
	apiPort, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_API_PORT")))
	if dataDir == "" || xrayPath == "" || generation == "" || err != nil || apiPort <= 0 {
		t.Fatalf("invalid Xray native accounting subprocess configuration: state=%q path=%q generation=%q api_port=%q", dataDir, xrayPath, generation, os.Getenv("ANTIMAGE_XRAY_NATIVE_API_PORT"))
	}
	s := newNativeXrayAccountingServer(t, dataDir, xrayPath, apiPort, generation)
	switch action {
	case "collect-first", "collect-next":
		batch, err := s.collectXrayUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if batch.GetBatchId() == "" || nativeXrayBatchValue(batch) == 0 {
			t.Fatalf("production Xray collector returned no positive native batch: %v", batch)
		}
		name := "native-first-batch.pb"
		if action == "collect-next" {
			name = "native-next-batch.pb"
		}
		writeNativeXrayBatch(t, dataDir, name, batch)
		t.Logf("fresh node-agent process emitted Xray batch %s with %d raw bytes", batch.GetBatchId(), nativeXrayBatchValue(batch))
	case "checkpoint":
		if err := s.checkpointXrayAccounting(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.xrayUsageMu.Lock()
		generationAfterCheckpoint := s.xrayAccountingGeneration
		s.xrayUsageMu.Unlock()
		if generationAfterCheckpoint != generation {
			t.Fatalf("Xray accounting checkpoint generation=%q want=%q", generationAfterCheckpoint, generation)
		}
		t.Logf("fresh node-agent process checkpointed Xray runtime generation %s without opening an empty billable batch", generation)
	case "ack":
		batchID := strings.TrimSpace(os.Getenv("ANTIMAGE_XRAY_NATIVE_BATCH_ID"))
		if batchID == "" {
			t.Fatal("ACK subprocess requires a batch ID")
		}
		ack, err := s.ackXrayUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batchID})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("DB-confirmed ACK of Xray batch %s: response=%v err=%v", batchID, ack, err)
		}
		reloaded := New(Config{DataDir: dataDir, XrayPath: xrayPath, XrayAPIPort: apiPort})
		reloaded.xrayUsageMu.Lock()
		err = reloaded.ensureXrayUsageStateLoadedLocked()
		pending := reloaded.xrayUsagePending
		lastAcked := reloaded.xrayUsageLastAckedBatchID
		reloaded.xrayUsageMu.Unlock()
		if err != nil || pending != nil || lastAcked != batchID {
			t.Fatalf("Xray ACK was not durable across another process reload: pending=%v last_acked=%q want=%q err=%v", pending, lastAcked, batchID, err)
		}
		t.Logf("fresh node-agent process durably ACKed and pruned Xray batch %s", batchID)
	default:
		t.Fatalf("unknown Xray native accounting subprocess action %q", action)
	}
}

func runNativeXrayAccountingStage(t *testing.T, action, dataDir, xrayPath string, apiPort int, generation, batchID string) *nodev1.UserUsageBatch {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestXrayNativeAccountingStage$", "-test.v")
	cmd.Env = append(os.Environ(),
		"ANTIMAGE_XRAY_NATIVE_ACTION="+action,
		"ANTIMAGE_XRAY_NATIVE_STATE="+dataDir,
		"ANTIMAGE_XRAY_NATIVE_PATH="+xrayPath,
		"ANTIMAGE_XRAY_NATIVE_API_PORT="+strconv.Itoa(apiPort),
		"ANTIMAGE_XRAY_NATIVE_GENERATION="+generation,
		"ANTIMAGE_XRAY_NATIVE_BATCH_ID="+batchID,
	)
	output, err := cmd.CombinedOutput()
	t.Logf("Xray native node-agent subprocess (%s):\n%s", action, output)
	if err != nil {
		t.Fatalf("Xray native node-agent subprocess %s: %v", action, err)
	}
	if action == "ack" || action == "checkpoint" {
		return nil
	}
	name := "native-first-batch.pb"
	if action == "collect-next" {
		name = "native-next-batch.pb"
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	batch := &nodev1.UserUsageBatch{}
	if err := proto.Unmarshal(raw, batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

func realXrayRuntimeGeneration(process *realXrayProcess) string {
	return fmt.Sprintf("native-xray-process-%d", process.cmd.Process.Pid)
}

func nativeXrayBatchValue(batch *nodev1.UserUsageBatch) uint64 {
	for _, sample := range batch.GetStats() {
		if sample.GetUid() == "xray:7" {
			return sample.GetValue()
		}
	}
	return 0
}

func writeNativeXrayBatch(t *testing.T, dir, name string, batch *nodev1.UserUsageBatch) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func runNativeXrayPanelDB(t *testing.T, binary, dir string, requireFinal bool) {
	t.Helper()
	cmd := exec.Command(binary, "-test.run=^TestXrayNativePanelDB$", "-test.v")
	cmd.Env = append(os.Environ(), "ANTIMAGE_XRAY_NATIVE_STATE="+dir)
	if requireFinal {
		cmd.Env = append(cmd.Env, "ANTIMAGE_NATIVE_PANEL_REQUIRE_FINAL=1")
	}
	output, err := cmd.CombinedOutput()
	t.Logf("Xray native SQLite exact-once result:\n%s", output)
	if err != nil {
		t.Fatalf("Xray native SQLite exact-once validation: %v", err)
	}
}

func assertNativeXrayPanelReceipt(t *testing.T, dir, batchID string, rawTotal uint64) {
	t.Helper()
	var receipt struct {
		BatchIDs       []string `json:"batch_ids"`
		RawTotal       uint64   `json:"raw_total"`
		EffectiveTotal uint64   `json:"effective_total"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "native-panel-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range receipt.BatchIDs {
		if id == batchID {
			found = true
		}
	}
	if !found || receipt.RawTotal != rawTotal || receipt.EffectiveTotal != rawTotal*3 {
		t.Fatalf("Xray Panel receipt does not reflect exact native batches: %+v want_batch=%s want_raw=%d", receipt, batchID, rawTotal)
	}
}

func nativeXrayUserCounterTotal(stats []xrayStat, email string) uint64 {
	var total uint64
	for _, stat := range stats {
		name, ok := parseXrayStatName(stat.Name)
		if ok && name.Type == "user" && name.Metric == "traffic" && name.Email == email && stat.Value > 0 {
			total += uint64(stat.Value)
		}
	}
	return total
}

func realXrayWaitForNativeCounterIncrease(t *testing.T, client *xrayStatsClient, email string, previous uint64) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var latest uint64
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stats, err := client.queryStats(ctx, "user>>>", false)
		cancel()
		if err == nil {
			latest = nativeXrayUserCounterTotal(stats, email)
			if latest > previous {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("native Xray counters for %q did not increase after real traffic: before=%d after=%d", email, previous, latest)
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

type realXrayProcess struct {
	cmd      *exec.Cmd
	logs     *realXrayLogBuffer
	waitOnce sync.Once
}

func (p *realXrayProcess) stop() {
	if p == nil || p.cmd == nil {
		return
	}
	p.waitOnce.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
	})
}

func realXrayStartProcess(
	t *testing.T,
	xrayPath string,
	configPath string,
) *realXrayProcess {
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

	process := &realXrayProcess{cmd: cmd, logs: logs}
	t.Cleanup(process.stop)
	return process
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
) xrayOnlineUserState {
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

		state, ok := users[email]
		if err == nil && ok && state.Count > 0 {
			return state
		}

		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf(
		"user %q never appeared in bulk OnlineMap; last error=%v",
		email,
		lastErr,
	)
	return xrayOnlineUserState{}
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
