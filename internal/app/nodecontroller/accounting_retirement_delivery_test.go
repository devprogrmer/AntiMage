package nodecontroller

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

func TestAccountingRetirementProofCapabilityKeepsLegacyNodeFallback(t *testing.T) {
	legacy := &nodeclient.Client{}
	if supportsAccountingRetirementProof(legacy) {
		t.Fatal("legacy node without the durable proof capability must keep its anchors")
	}
	current := &nodeclient.Client{}
	current.SetHandshake("test", []string{"accounting_retirement_proof_v1"})
	if !supportsAccountingRetirementProof(current) {
		t.Fatal("node advertising durable proof support was not recognized")
	}
}

type retirementProofRPC struct {
	nodev1.NodeUsageServiceClient
	ids      []string
	err      error
	response *nodev1.RetireAccountingAnchorsResponse
}

func (r *retirementProofRPC) RetireAccountingAnchors(_ context.Context, req *nodev1.RetireAccountingAnchorsRequest, _ ...grpc.CallOption) (*nodev1.RetireAccountingAnchorsResponse, error) {
	r.ids = r.ids[:0]
	for _, proof := range req.GetProofs() {
		r.ids = append(r.ids, proof.GetProofId())
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.response, nil
}

func TestRetirementProofDeliveryRetriesLostACKAndPersistsOnlyAccepted(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "retirement-delivery.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createUsageTables(t, ctx, db)
	proofID := "durable-proof-1"
	if _, err := db.ExecContext(ctx, `INSERT INTO node_usage_retirement_proofs(proof_id,node_id,user_id,epoch,owner_hash,owner_key,total_raw,legacy_raw,created_at) VALUES(?,7,10,'epoch-a','hash-a','"xray":"in"',150,100,CURRENT_TIMESTAMP)`, proofID); err != nil {
		t.Fatal(err)
	}
	controller := NewController(NewRepository(db, "sqlite"))
	rpc := &retirementProofRPC{err: context.DeadlineExceeded}
	if err := controller.deliverAccountingRetirementProofs(ctx, rpc, 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost ACK error=%v", err)
	}
	if len(rpc.ids) != 1 || rpc.ids[0] != proofID {
		t.Fatalf("first delivery ids=%v", rpc.ids)
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_retirement_proofs WHERE proof_id='durable-proof-1' AND delivered_at IS NULL`, 1)

	// A restart/retry resends the same proof. It is considered delivered only
	// after the node returns the exact proof identity in its durable response.
	rpc.err = nil
	rpc.response = &nodev1.RetireAccountingAnchorsResponse{Acknowledged: true, AcceptedProofIds: []string{proofID}}
	if err := NewController(NewRepository(db, "sqlite")).deliverAccountingRetirementProofs(ctx, rpc, 7); err != nil {
		t.Fatal(err)
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_retirement_proofs WHERE proof_id='durable-proof-1' AND delivered_at IS NOT NULL`, 1)
}

func TestRetirementProofDeliveryRejectsUnknownOrPartialACK(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "retirement-delivery-partial.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createUsageTables(t, ctx, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO node_usage_retirement_proofs(proof_id,node_id,user_id,epoch,owner_hash,owner_key,total_raw,legacy_raw,created_at) VALUES('durable-proof-2',7,10,'epoch-a','hash-a','"xray":"in"',150,100,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	controller := NewController(NewRepository(db, "sqlite"))
	rpc := &retirementProofRPC{response: &nodev1.RetireAccountingAnchorsResponse{Acknowledged: true, AcceptedProofIds: []string{"other-proof"}}}
	if err := controller.deliverAccountingRetirementProofs(ctx, rpc, 7); err == nil {
		t.Fatal("expected unknown ACK identity rejection")
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_retirement_proofs WHERE delivered_at IS NULL`, 1)
}
