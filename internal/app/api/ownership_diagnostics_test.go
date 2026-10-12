//go:build cgo

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/diagnostics"
	operationapp "github.com/antimage/antimage/internal/app/operations"
)

func TestOwnershipDiagnosticsDefiniteOrphansAndExhaustedRecovery(t *testing.T) {
	s, db := testAdminServer(t)
	for _, ddl := range []string{
		`CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`,
		`CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
		`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT,payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
		operationapp.ExecutorLeaseDDL,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	op := operationapp.Operation{ID: "terminal", Type: "node_update", TargetType: "node", TargetID: "2", State: "completed", Phase: "completed", CreatedAt: 1, UpdatedAt: 1, Metadata: map[string]any{}}
	if err := operationapp.Upsert(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	op.ID, op.TargetID, op.State, op.Phase = "mismatch", "3", "running", "running"
	if err := operationapp.Upsert(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	op.ID, op.TargetID, op.State, op.Phase = "manual", "7", "waiting", "manual_recovery_required"
	op.UpdatedAt = time.Now().Add(-2 * time.Minute).Unix()
	op.Metadata = map[string]any{"recovery_attempts": 3, "raw_error": "secret-marker"}
	if err := operationapp.Upsert(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operation_locks VALUES('node','1','missing'),('node','2','terminal'),('node','4','mismatch'),('node','7','manual')`); err != nil {
		t.Fatal(err)
	}
	var findings []diagnostics.Observation
	complete, err := s.collectOwnershipDiagnostics(ctx, &findings)
	if err != nil || !complete {
		t.Fatalf("collector: %v %v", complete, err)
	}
	orphans, critical := 0, 0
	for _, finding := range findings {
		if finding.Code == "lock.orphan_detected" {
			orphans++
		}
		if finding.Code == "recovery.manual_required" && finding.Severity == "critical" {
			critical++
		}
		if strings.Contains(finding.Detail, "secret-marker") {
			t.Fatal("raw error exposed")
		}
	}
	if orphans != 3 || critical != 1 {
		t.Fatalf("findings: %+v", findings)
	}
	repairs, err := operationapp.RepairOrphanLocks(ctx, db)
	if err != nil || len(repairs) != 3 {
		t.Fatalf("repair: %+v %v", repairs, err)
	}
	findings = nil
	if _, err := s.collectOwnershipDiagnostics(ctx, &findings); err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Code == "lock.orphan_detected" {
			t.Fatal("repaired orphan still reported")
		}
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_locks WHERE operation_id='manual'`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("unknown outcome ownership removed: %d %v", remaining, err)
	}
}
