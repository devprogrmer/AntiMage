//go:build cgo

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/diagnostics"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	operationapp "github.com/antimage/antimage/internal/app/operations"
)

func TestUpdateLifecycleDiagnosticsCriticalRollbackAndResolution(t *testing.T) {
	server, db := testAdminServer(t)
	if _, err := db.Exec(`CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	op := operationapp.Operation{ID: "rollback-failed", Type: "node_rollback", TargetType: "node", TargetID: "7", State: "failed", Phase: "failed", CreatedAt: now, UpdatedAt: now, Metadata: map[string]any{"update": nodecontroller.NodeUpdateOperation{RollbackError: "secret deliberately omitted"}}}
	if err := operationapp.Upsert(context.Background(), db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	var findings []diagnostics.Observation
	if err := server.collectUpdateLifecycleDiagnostics(context.Background(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != "critical" || findings[0].Code != "update.rollback_failed" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	op.ID = "rollback-success"
	op.State = "rolled_back"
	op.Phase = "rolled_back"
	op.CreatedAt = now + 1
	op.Metadata = map[string]any{"update": nodecontroller.NodeUpdateOperation{}}
	if err := operationapp.Upsert(context.Background(), db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	findings = nil
	if err := server.collectUpdateLifecycleDiagnostics(context.Background(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("superseded failure remained active: %+v", findings)
	}
}

func TestUpdateRecoveryDiagnosticsUsesDeadlineAndOmitsRawErrors(t *testing.T) {
	server, db := testAdminServer(t)
	if _, err := db.Exec(`CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	op := operationapp.Operation{ID: "recovery", Type: "node_update", TargetType: "node", TargetID: "7", State: "failed", Phase: "failed", CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Metadata: map[string]any{"update": nodecontroller.NodeUpdateOperation{RecoveryError: "Authorization: secret recovery output", RollbackError: "secret"}}}
	if err := operationapp.Upsert(context.Background(), db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	var findings []diagnostics.Observation
	if err := server.collectUpdateLifecycleDiagnostics(context.Background(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Code != "update.recovery_failed" || findings[0].Severity != "critical" {
		t.Fatalf("recovery failure lost: %+v", findings)
	}
	if strings.Contains(findings[0].Detail, "secret") || strings.Contains(findings[0].Detail, "Authorization") {
		t.Fatal("raw error exposed in diagnostics")
	}
	op.State, op.Phase = "waiting", "waiting_for_reconnect"
	op.Metadata = map[string]any{"update": nodecontroller.NodeUpdateOperation{ReconnectDeadline: now.Add(-time.Second)}}
	if err := operationapp.Upsert(context.Background(), db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	findings = nil
	if err := server.collectUpdateLifecycleDiagnostics(context.Background(), &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Code != "update.reconnect_stuck" {
		t.Fatalf("persisted deadline ignored because updated_at is recent: %+v", findings)
	}
}
