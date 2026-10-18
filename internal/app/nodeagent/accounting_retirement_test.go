package nodeagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"github.com/antimage/antimage/internal/platform/usagereflection"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	_ "modernc.org/sqlite"
)

func retirementTestServer(t *testing.T) (*Server, *nodev1.AccountingRetirementProof) {
	t.Helper()
	cfg := Config{DataDir: t.TempDir()}
	xrayKey := usagereflection.Key("xray", "deleted-in")
	server := &Server{
		cfg:              cfg,
		localUsageLoaded: true,
		localUsageV2: &localUsageLedgerV2{
			Epoch:  "retirement-epoch",
			Latest: map[string]localUsageReceiptV2{},
			Owners: []localUsageOwnerV2{
				{Protocol: "xray", UserID: 10, InboundTag: "deleted-in", Total: 150, LegacyTotal: 100, LastChild: "xray-latest", LastRaw: 50},
				{Protocol: "wireguard", UserID: 10, InboundTag: "active-in", Total: 70, LegacyTotal: 70, LastChild: "wg-latest", LastRaw: 70},
				{Protocol: "openvpn", UserID: 11, InboundTag: "other-user", Total: 200, LegacyTotal: 200, LastChild: "ovpn-latest", LastRaw: 200},
			},
		},
		localUsageDeliveries: []localUsageDelivery{
			{BatchID: "combined-legacy", Rows: []localUsageDeliveryRow{
				{Protocol: "xray", UserID: 10, InboundTag: "deleted-in", ChildID: "xray-old", Raw: 100},
				{Protocol: "wireguard", UserID: 10, InboundTag: "active-in", ChildID: "wg-old", Raw: 70},
				{Protocol: "openvpn", UserID: 11, InboundTag: "other-user", ChildID: "ovpn-old", Raw: 200},
			}},
		},
	}
	if err := server.persistLocalUsageDeliveriesLocked(server.localUsageDeliveries); err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"target":"node:7","session_callback":{"node_id":7}}`}); err != nil {
		t.Fatal(err)
	}
	proof := &nodev1.AccountingRetirementProof{
		NodeId: 7, UserId: 10, Epoch: "retirement-epoch", OwnerKey: xrayKey, TotalRaw: 150, LegacyRaw: 100,
	}
	proof.ProofId = usagereflection.RetirementProofID(proof.NodeId, proof.UserId, proof.Epoch, proof.OwnerKey, proof.TotalRaw, proof.LegacyRaw)
	return server, proof
}

func TestRetireAccountingAnchorsPersistsProofAndPreservesOtherOwners(t *testing.T) {
	server, proof := retirementTestServer(t)
	beforeTotal := server.localUsageV2.Owners[0].Total
	response, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
	if err != nil || !response.GetAcknowledged() || len(response.GetAcceptedProofIds()) != 1 || response.GetAcceptedProofIds()[0] != proof.GetProofId() {
		t.Fatalf("retirement response=%+v err=%v", response, err)
	}
	if got := server.localUsageV2.Owners[0]; got.Total != beforeTotal || got.LegacyTotal != 0 || got.RetiredLegacyRaw != 100 || got.RetiredThrough != 150 || got.RetirementProofID != proof.GetProofId() {
		t.Fatalf("retired owner state=%+v", got)
	}
	if len(server.localUsageDeliveries) != 1 || len(server.localUsageDeliveries[0].Rows) != 2 {
		t.Fatalf("unrelated owner anchors were removed: %+v", server.localUsageDeliveries)
	}
	for _, row := range server.localUsageDeliveries[0].Rows {
		if row.UserID == 10 && row.Protocol == "xray" {
			t.Fatal("target legacy anchor remains")
		}
	}
	if server.localUsageV2.Owners[1].Total != 70 || server.localUsageV2.Owners[2].Total != 200 {
		t.Fatal("unrelated active/deleted owner cumulative bytes changed")
	}

	// Simulate a lost ACK and node restart: the persisted proof identity makes
	// the replay idempotent and the removed anchor cannot reappear.
	restarted := &Server{cfg: server.cfg}
	response, err = restarted.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof, proof}})
	if err != nil || !response.GetAcknowledged() || len(response.GetAcceptedProofIds()) != 1 {
		t.Fatalf("duplicate replay response=%+v err=%v", response, err)
	}
	if restarted.localUsageV2.Owners[0].Total != beforeTotal || restarted.localUsageV2.Owners[0].RetiredLegacyRaw != 100 || len(restarted.localUsageDeliveries) != 1 || len(restarted.localUsageDeliveries[0].Rows) != 2 {
		t.Fatal("restart/replay changed conserved bytes or restored compacted anchor")
	}
}

func TestRetireAccountingAnchorsRejectsStaleOrIncompleteProof(t *testing.T) {
	t.Run("stale cumulative total preserves anchor", func(t *testing.T) {
		server, proof := retirementTestServer(t)
		proof.TotalRaw--
		proof.ProofId = usagereflection.RetirementProofID(proof.NodeId, proof.UserId, proof.Epoch, proof.OwnerKey, proof.TotalRaw, proof.LegacyRaw)
		_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("stale proof error=%v", err)
		}
		if server.localUsageV2.Owners[0].LegacyTotal != 100 || len(server.localUsageDeliveries[0].Rows) != 3 {
			t.Fatal("stale proof removed a pending legacy anchor")
		}
	})
	t.Run("anchor byte mismatch preserves all state", func(t *testing.T) {
		server, proof := retirementTestServer(t)
		server.localUsageDeliveries[0].Rows[0].Raw = 99
		_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("mismatched anchor error=%v", err)
		}
		if server.localUsageV2.Owners[0].LegacyTotal != 100 || len(server.localUsageDeliveries[0].Rows) != 3 {
			t.Fatal("mismatched proof altered local accounting")
		}
	})
	t.Run("proof ahead of durable owner is rejected", func(t *testing.T) {
		server, proof := retirementTestServer(t)
		proof.TotalRaw++
		proof.ProofId = usagereflection.RetirementProofID(proof.NodeId, proof.UserId, proof.Epoch, proof.OwnerKey, proof.TotalRaw, proof.LegacyRaw)
		_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("future proof error=%v", err)
		}
		if server.localUsageV2.Owners[0].LegacyTotal != 100 {
			t.Fatal("future proof removed anchor")
		}
	})
}

func TestRetireAccountingAnchorsPersistenceFailureRollsBackMemory(t *testing.T) {
	server, proof := retirementTestServer(t)
	accountingDir := filepath.Join(server.cfg.DataDir, "accounting")
	if err := os.Rename(accountingDir, accountingDir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountingDir, []byte("block directory creation"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
	if status.Code(err) != codes.Internal {
		t.Fatalf("persistence failure error=%v", err)
	}
	if server.localUsageV2.Owners[0].LegacyTotal != 100 || server.localUsageV2.Owners[0].RetiredLegacyRaw != 0 || len(server.localUsageDeliveries[0].Rows) != 3 {
		t.Fatal("failed persistence left partially retired in-memory state")
	}
}

func TestRetireAccountingAnchorsAcceptsPanelProofAfterPriorReflectionCompaction(t *testing.T) {
	server, proof := retirementTestServer(t)
	if err := server.compactLegacyOwnersLocked(map[localUsageOwnerKey]bool{{Protocol: "xray", UserID: 10, InboundTag: "deleted-in"}: true}); err != nil {
		t.Fatal(err)
	}
	response, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
	if err != nil || !response.GetAcknowledged() {
		t.Fatalf("proof for already-reflected anchor response=%+v err=%v", response, err)
	}
	if server.localUsageV2.Owners[0].RetiredLegacyRaw != 100 || len(server.localUsageDeliveries[0].Rows) != 2 {
		t.Fatal("already-reflected proof changed unrelated ledger rows")
	}
}

func TestRetireAccountingAnchorsRejectsProofForDifferentNode(t *testing.T) {
	server, proof := retirementTestServer(t)
	proof.NodeId = 8
	proof.ProofId = usagereflection.RetirementProofID(proof.NodeId, proof.UserId, proof.Epoch, proof.OwnerKey, proof.TotalRaw, proof.LegacyRaw)
	_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cross-node proof error=%v", err)
	}
	if server.localUsageV2.Owners[0].LegacyTotal != 100 || len(server.localUsageDeliveries[0].Rows) != 3 {
		t.Fatal("cross-node proof changed local anchors")
	}
}

func TestRetireAccountingAnchorsRejectsMissingRuntimeIdentity(t *testing.T) {
	server, proof := retirementTestServer(t)
	server.runtimePolicy = &runtimePolicySnapshot{NativeJSON: string(mustJSONBytes(t, nativeRuntimePayload{}))}
	_, err := server.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("missing identity error=%v", err)
	}
}

func TestRuntimeStateAdvertisesDurableAccountingRetirementProofRPC(t *testing.T) {
	state := (&Server{cfg: Config{DataDir: t.TempDir()}}).runtimeState("test")
	for _, capability := range state.GetCapabilities() {
		if capability == "accounting_retirement_proof_v1" {
			return
		}
	}
	t.Fatal("durable accounting retirement RPC capability was not advertised")
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCommittedPanelProofRetiresNodeAnchorOverGRPCWithLostACKAndRestart(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "retirement-e2e.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,status,used_traffic,data_limit) VALUES(10,'retired-user','deleted',123,100000); INSERT INTO nodes(id,name,address,port,api_port,status,usage_coefficient,uplink,downlink) VALUES(7,'node-7','127.0.0.1',62050,10085,'connected',1,0,0); INSERT INTO user_accounting_tombstones(user_id,deleted_at) VALUES(10,CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}
	ownerKey := usagereflection.Key("xray", "deleted-in")
	encoded, err := json.Marshal(usagereflection.Watermark{
		Version: 2, Epoch: "retirement-epoch", Root: "combined-retirement-e2e",
		Totals: map[string]uint64{ownerKey: 150}, Baselines: map[string]uint64{ownerKey: 0},
		LegacyBatches:   map[string]map[string]uint64{ownerKey: {"combined-old-anchor": 100}},
		PendingChildren: map[string]usagereflection.PendingChild{ownerKey: {BatchID: "xray-latest", Raw: 50}},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := nodecontroller.NewRepository(db, "sqlite")
	delta := nodecontroller.UserUsageDelta{UserID: 10, Value: 50, RawValue: 50, Online: true, AccountingProtocol: "xray", AccountingChildID: "xray-latest", InboundTag: "deleted-in", InboundCoefficient: 1}
	if err := repo.StoreCollectedUsage(ctx, nodecontroller.NodeRow{ID: 7, UsageCoefficient: 1}, "combined-retirement-e2e", []nodecontroller.UserUsageDelta{delta}, "", nil, nodecontroller.UsagePersistOptions{ReflectionReceipts: map[int64]string{10: string(encoded)}}); err != nil {
		t.Fatalf("commit tombstone retirement receipt: %v", err)
	}
	proofs, err := repo.PendingAccountingRetirementProofs(ctx, 7)
	if err != nil || len(proofs) != 1 {
		t.Fatalf("committed panel proof count=%d err=%v", len(proofs), err)
	}
	agent, _ := retirementTestServer(t)
	var dropACK atomic.Bool
	dropACK.Store(true)
	client := startAccountingRetirementGRPC(t, agent, func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err == nil && strings.HasSuffix(method, "/RetireAccountingAnchors") && dropACK.CompareAndSwap(true, false) {
			return context.DeadlineExceeded
		}
		return err
	})
	if _, err := client.RetireAccountingAnchors(ctx, &nodev1.RetireAccountingAnchorsRequest{Proofs: proofs}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("simulated lost RPC ACK error=%v", err)
	}
	if agent.localUsageV2.Owners[0].LegacyTotal != 0 || agent.localUsageV2.Owners[0].Total != 150 {
		t.Fatal("node did not persist anchor retirement before lost ACK")
	}
	assertRetirementDBInt64(t, db, `SELECT COUNT(*) FROM node_usage_retirement_proofs WHERE node_id=7 AND delivered_at IS NULL`, 1)

	// Restart NodeAgent after its durable write, retry the same proof over a new
	// gRPC connection, and mark the panel row delivered only after that ACK.
	restartedAgent := &Server{cfg: agent.cfg}
	retryClient := startAccountingRetirementGRPC(t, restartedAgent, nil)
	response, err := retryClient.RetireAccountingAnchors(ctx, &nodev1.RetireAccountingAnchorsRequest{Proofs: proofs})
	if err != nil || !response.GetAcknowledged() || len(response.GetAcceptedProofIds()) != 1 || response.GetAcceptedProofIds()[0] != proofs[0].GetProofId() {
		t.Fatalf("retry after NodeAgent restart response=%+v err=%v", response, err)
	}
	if err := repo.MarkAccountingRetirementProofsDelivered(ctx, 7, response.GetAcceptedProofIds()); err != nil {
		t.Fatal(err)
	}
	assertRetirementDBInt64(t, db, `SELECT COUNT(*) FROM node_usage_retirement_proofs WHERE node_id=7 AND delivered_at IS NOT NULL`, 1)
	assertRetirementDBInt64(t, db, `SELECT used_traffic FROM users WHERE id=10`, 123)
	if restartedAgent.localUsageV2.Owners[0].Total != 150 || restartedAgent.localUsageV2.Owners[0].RetiredLegacyRaw != 100 || len(restartedAgent.localUsageDeliveries[0].Rows) != 2 {
		t.Fatal("end-to-end retirement changed byte totals or removed unrelated owner rows")
	}
}

func assertRetirementDBInt64(t *testing.T, db *sql.DB, query string, expected int64) {
	t.Helper()
	var actual int64
	if err := db.QueryRow(query).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("%s: got %d, want %d", query, actual, expected)
	}
}

func startAccountingRetirementGRPC(t *testing.T, agent *Server, interceptor grpc.UnaryClientInterceptor) nodev1.NodeUsageServiceClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	nodev1.RegisterNodeUsageServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	options := []grpc.DialOption{
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if interceptor != nil {
		options = append(options, grpc.WithUnaryInterceptor(interceptor))
	}
	conn, err := grpc.Dial("bufnet", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return nodev1.NewNodeUsageServiceClient(conn)
}
