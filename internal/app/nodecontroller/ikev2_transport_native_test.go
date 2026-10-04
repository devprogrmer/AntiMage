package nodecontroller

// This opt-in test is the panel-side process entry point for the WSL native
// IKEv2 driver. It deliberately uses Controller.CollectUsage and Repository;
// it is not a fake transport or an in-process node substitute. The driver can
// kill/restart the node between ticks and restart this process while retaining
// the same SQLite database.

import (
	"context"
	"encoding/json"
	"strings"

	"database/sql"
	"fmt"
	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIKEv2NativePanelControllerProcess(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("ANTIMAGE_IKEV2_NATIVE_PANEL_PROCESS") != "1" {
		t.Skip("requires the WSL native IKEv2 panel process harness")
	}
	dir := os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")
	if dir == "" {
		t.Fatal("ANTIMAGE_IKEV2_NATIVE_STATE is required")
	}
	dbPath := os.Getenv("ANTIMAGE_IKEV2_NATIVE_DB")
	if dbPath == "" {
		dbPath = filepath.Join(dir, "native-panel.db")
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	controller := NewController(NewRepository(db, "sqlite"))
	nodeID := int64(9)
	if raw := os.Getenv("ANTIMAGE_IKEV2_NATIVE_NODE_ID"); raw != "" {
		nodeID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || nodeID <= 0 {
			t.Fatalf("invalid ANTIMAGE_IKEV2_NATIVE_NODE_ID: %q", raw)
		}
	}
	if stage := os.Getenv("ANTIMAGE_IKEV2_NATIVE_TRANSPORT_STAGE"); stage != "" {
		nativeTransportStage(t, ctx, controller, db, dir, nodeID, stage)
		return
	}
	interval := 250 * time.Millisecond
	if raw := os.Getenv("ANTIMAGE_IKEV2_NATIVE_PANEL_INTERVAL"); raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			t.Fatalf("invalid ANTIMAGE_IKEV2_NATIVE_PANEL_INTERVAL: %q", raw)
		}
	}
	ready := os.Getenv("ANTIMAGE_IKEV2_NATIVE_PANEL_READY")
	if ready == "" {
		ready = filepath.Join(dir, "native-panel-ready")
	}
	if err := os.WriteFile(ready, []byte(fmt.Sprintf("node=%d\n", nodeID)), 0600); err != nil {
		t.Fatal(err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, collectErr := controller.CollectUsage(ctx, CollectUsageRequest{
			NodeID:  nodeID,
			Users:   true,
			NoReset: false,
		})
		if collectErr != nil {
			t.Logf("controller collection retry: %v", collectErr)
		} else if len(result.Errors) > 0 {
			t.Logf("controller collection degraded; retrying: %v", result.Errors)
		} else {
			t.Logf("controller collection: nodes=%d batches=%d acked=%d", result.Nodes, result.UserBatches, result.UserAcked)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stages are invoked in separate processes against one DB: drop-ack, outage
// (with node dead), retry-drop-ack (after node restart), retry-ack. The first
// stage prunes the processed queue before replay, exercising tombstones too.
// Only the ACK request is fault-injected, before transmission; every collected
// byte and batch ID comes from the real Server.Run TLS endpoint.
func nativeTransportStage(t *testing.T, parent context.Context, c Controller, db *sql.DB, dir string, nodeID int64, stage string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if stage == "outage" {
		result, err := c.CollectUsage(ctx, CollectUsageRequest{NodeID: nodeID, Users: true})
		if err == nil && len(result.Errors) == 0 {
			t.Fatalf("expected dead node transport failure: %+v", result)
		}
		t.Logf("verified unavailable node: %v %+v", err, result)
		return
	}
	if stage != "drop-ack" && stage != "retry-drop-ack" && stage != "retry-ack" {
		t.Fatalf("unknown native transport stage %q", stage)
	}
	_, node, err := c.dial(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	cachedValue, ok := c.nodeClients.Load(nodeID)
	if !ok {
		t.Fatal("production controller did not cache client")
	}
	cached := cachedValue.(cachedNodeClient)
	tlsRow, err := c.repo.TLS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cert := firstNonEmpty(node.Certificate, tlsRow.Certificate)
	key := firstNonEmpty(node.CertificateKey, tlsRow.Key)
	tlsConfig, err := nodeclient.LoadClientTLSFromPEM(nodeclient.PEMTLSConfig{ClientCertPEM: cert, ClientKeyPEM: key, ServerCertPEM: cert})
	if err != nil {
		t.Fatal(err)
	}
	var batch *nodev1.UserUsageBatch
	var dropped bool
	interceptor := func(ctx context.Context, method string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if strings.HasSuffix(method, "/AckUserUsage") && stage != "retry-ack" {
			dropped = true
			return status.Error(codes.Unavailable, "native harness dropped ACK before transmission")
		}
		err := invoke(ctx, method, req, reply, conn, options...)
		if err == nil && strings.HasSuffix(method, "/CollectUserUsage") {
			batch = proto.Clone(reply.(*nodev1.UserUsageBatch)).(*nodev1.UserUsageBatch)
		}
		return err
	}
	addresses := NodeGRPCAddressCandidates(node.Address, node.Port, node.APIPort)
	client, err := nodeclient.Dial(ctx, addresses[0], tlsConfig, grpc.WithBlock(), grpc.WithUnaryInterceptor(interceptor))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetHandshake(cached.client.NodeVersion(), nil)
	c.nodeClients.Store(nodeID, cachedNodeClient{key: cached.key, client: client})
	_ = cached.client.Close()
	result, err := c.CollectUsage(ctx, CollectUsageRequest{NodeID: nodeID, Users: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range result.Errors {
		if !strings.Contains(failure, "native harness dropped ACK") {
			t.Fatalf("unexpected collection failure: %s", failure)
		}
	}
	if batch == nil || batch.BatchId == "" {
		t.Fatalf("no live batch: %+v", result)
	}
	if stage == "retry-ack" {
		if result.UserAcked != 1 {
			t.Fatalf("ACK not committed: %+v", result)
		}
	} else if !dropped || result.UserAcked != 0 {
		t.Fatalf("ACK fault not exercised: %+v", result)
	}
	var total uint64
	for _, sample := range batch.Stats {
		id, online, valid := parseUserUsageSampleUID(sample.Uid)
		if !valid || id != 7 || sample.InboundTag != "native" {
			t.Fatalf("unexpected live sample: %v", sample)
		}
		if !online {
			total += sample.Value
		}
	}
	if total == 0 {
		t.Fatal("real traffic required; zero byte batches are not lifecycle evidence")
	}
	path := filepath.Join(dir, "native-transport-first.pb")
	if stage == "drop-ack" {
		raw, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		first := new(nodev1.UserUsageBatch)
		if err := proto.Unmarshal(raw, first); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(first, batch) {
			t.Fatalf("pending changed across process restart: first=%v retry=%v", first, batch)
		}
	}
	if _, err := c.repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := c.repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
		t.Fatal(err)
	}
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=7`, int64(total*3))
	assertInt64(t, db, fmt.Sprintf(`SELECT SUM(used_traffic) FROM node_user_usages WHERE user_id=7 AND node_id=%d`, nodeID), int64(total*3))
	assertInt64(t, db, `SELECT users_usage FROM admins WHERE id=1`, int64(total*3))
	if stage == "drop-ack" {
		count, err := c.repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100)
		if err != nil || count != 1 {
			t.Fatalf("prune: count=%d error=%v", count, err)
		}
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_user_queue`, 0)
	if stage == "retry-ack" {
		next, err := client.Usage().CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if next.BatchId == batch.BatchId {
			t.Fatal("ACK did not prune node pending batch")
		}
	}
	receipt, err := json.Marshal(map[string]any{"stage": stage, "batch_id": batch.BatchId, "raw_total": total, "effective_total": total * 3, "acked": result.UserAcked})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native-transport-"+stage+".json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified native transport stage: %s", receipt)
}
