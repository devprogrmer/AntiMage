package nodecontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestDelayedFencedServiceResponseCannotOverwriteTakeover(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	op := NodeUpdateOperation{ID: "delayed-result", NodeID: 1, Phase: "waiting_for_reconnect"}
	if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	first, err := operationapp.AcquireExecutorLease(ctx, c.repo.db, "sqlite", op.ID, "controller-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.repo.db.Exec(`UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
		t.Fatal(err)
	}
	second, err := operationapp.AcquireExecutorLease(ctx, c.repo.db, "sqlite", op.ID, "controller-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.repo.AdvanceNodeUpdate(operationapp.WithExecutorLease(ctx, second), op.ID, "completed", 100, "", "v1.2.4", "v1.2.4", "", "", true); err != nil {
		t.Fatal(err)
	}
	request := &nodev1.ServiceUpdateRequest{OperationId: op.ID, FenceOperationId: op.ID, CommandId: "command-a", LeaseGeneration: first.Generation, ResourceGeneration: first.ResourceGeneration, ResourceId: first.TargetID}
	response := &nodev1.RuntimeActionResponse{OperationId: op.ID, CommandId: request.CommandId, LeaseGeneration: first.Generation, ResourceGeneration: first.ResourceGeneration, ResourceId: first.TargetID, Accepted: true}
	if err := c.validateFencedServiceResult(operationapp.WithExecutorLease(ctx, first), op.ID, request, response, nil); !errors.Is(err, operationapp.ErrLeaseLost) {
		t.Fatalf("delayed stale response accepted: %v", err)
	}
	stored, err := c.repo.NodeUpdateOperation(ctx, op.ID)
	if err != nil || stored.Phase != "completed" || stored.RunningVersion != "v1.2.4" {
		t.Fatalf("takeover result overwritten: %+v %v", stored, err)
	}
}

func TestFencedServiceResultRequiresExactEcho(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	op := NodeUpdateOperation{ID: "echo", NodeID: 1, Phase: "preflight"}
	if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	lease, _ := operationapp.ExecutorLeaseFromContext(worker)
	request := &nodev1.ServiceUpdateRequest{OperationId: op.ID, FenceOperationId: op.ID, CommandId: "command-a", LeaseGeneration: lease.Generation, ResourceGeneration: lease.ResourceGeneration, ResourceId: lease.TargetID}
	for _, response := range []*nodev1.RuntimeActionResponse{nil, {OperationId: op.ID}, {OperationId: "wrong", CommandId: request.CommandId, LeaseGeneration: lease.Generation}, {OperationId: op.ID, CommandId: "wrong", LeaseGeneration: lease.Generation}, {OperationId: op.ID, CommandId: request.CommandId, LeaseGeneration: lease.Generation + 1}} {
		if err := c.validateFencedServiceResult(worker, op.ID, request, response, nil); err == nil {
			t.Fatalf("invalid echo accepted: %+v", response)
		}
	}
	response := &nodev1.RuntimeActionResponse{OperationId: op.ID, CommandId: request.CommandId, LeaseGeneration: lease.Generation, ResourceGeneration: lease.ResourceGeneration, ResourceId: lease.TargetID}
	if err := c.validateFencedServiceResult(worker, op.ID, request, response, nil); err != nil {
		t.Fatal(err)
	}
	request.LeaseGeneration++
	response.LeaseGeneration = request.LeaseGeneration
	if err := c.validateFencedServiceResult(worker, op.ID, request, response, nil); !errors.Is(err, operationapp.ErrLeaseLost) {
		t.Fatalf("matching echo for another generation accepted: %v", err)
	}
}

func TestLostACKKeepsOutcomeUnknownAndOriginalDeadline(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	op := NodeUpdateOperation{ID: "lost-ack", NodeID: 1, Phase: "preflight"}
	if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	boundary := time.Now().UTC().Add(-time.Minute)
	if err := c.repo.RecordNodeRestart(worker, op.ID, boundary); err != nil {
		t.Fatal(err)
	}
	lease, _ := operationapp.ExecutorLeaseFromContext(worker)
	request := &nodev1.ServiceUpdateRequest{OperationId: op.ID, FenceOperationId: op.ID, CommandId: "command-lost", LeaseGeneration: lease.Generation, ResourceGeneration: lease.ResourceGeneration, ResourceId: lease.TargetID}
	canceled, cancel := context.WithCancel(worker)
	cancel()
	err = c.validateFencedServiceResult(canceled, op.ID, request, nil, context.DeadlineExceeded)
	if !errors.Is(err, ErrCommandOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost ACK incorrectly classified: %v", err)
	}
	if err := c.repo.AdvanceNodeUpdate(worker, op.ID, "outcome_unknown", 50, "", "", "", "reconciliation required", "", false); err != nil {
		t.Fatal(err)
	}
	stored, err := c.repo.NodeUpdateOperation(ctx, op.ID)
	if err != nil || stored.CompletedAt != nil || !stored.ReconnectDeadline.Equal(boundary.Add(4*time.Minute)) || !stored.RestartRequestedAt.Equal(boundary) {
		t.Fatalf("unknown outcome lost original boundaries: %+v %v", stored, err)
	}
	if RecoveryPolicy(stored.Phase) != "must_reconcile_external_state" {
		t.Fatal("unknown outcome permits blind replay")
	}
	if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "conflicting-update", NodeID: 1}); err == nil {
		t.Fatal("unknown outcome released destructive resource")
	}
}

func TestUnknownOutcomeNotRequeuedByStaleRunningRecovery(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	for _, ddl := range []string{
		`ALTER TABLE nodes ADD COLUMN status TEXT DEFAULT 'connected'`,
		`ALTER TABLE node_operations ADD COLUMN last_error TEXT`,
		`ALTER TABLE node_operations ADD COLUMN updated_at DATETIME`,
		`ALTER TABLE node_operations ADD COLUMN attempts INTEGER DEFAULT 0`,
	} {
		if _, err := c.repo.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.repo.db.Exec(`INSERT INTO node_operations(id,operation_type,node_id,status,updated_at) VALUES(1,'update_service',1,'running',?)`, c.repo.timeArg(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := c.repo.MarkOperationOutcomeUnknown(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.repo.db.Exec(`UPDATE node_operations SET updated_at=? WHERE id=1`, c.repo.timeArg(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := c.repo.RecoverStaleOperations(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := c.repo.db.QueryRow(`SELECT status FROM node_operations WHERE id=1`).Scan(&state); err != nil || state != "outcome_unknown" {
		t.Fatalf("unknown outcome entered retry queue: %s %v", state, err)
	}
	claimed, err := c.repo.MarkOperationRunning(ctx, 1)
	if err != nil || claimed {
		t.Fatalf("unknown command was redispatched: %v %v", claimed, err)
	}
}
