package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestExclusiveOperationLockSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "locks.sqlite")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	db := open()
	for _, ddl := range []string{
		ExecutorLeaseDDL,
		ResourceFenceDDL,
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`,
		`CREATE TABLE operation_locks (target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
		`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	op := Operation{ID: "first", Type: "node_update", TargetType: "node", TargetID: "7", RequestedBy: "owner", RequestID: "request-original", State: "queued", Phase: "queued", CreatedAt: 1, UpdatedAt: 1, Metadata: map[string]any{"version": "v1.2.3"}}
	if err := CreateExclusive(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	other := op
	other.ID = "second"
	err := CreateExclusive(ctx, db, "sqlite", other)
	var conflict Conflict
	if !errors.As(err, &conflict) || conflict.BlockingID != op.ID {
		t.Fatalf("conflict=%v", err)
	}
	if _, err := Get(ctx, db, other.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("blocked operation persisted: %v", err)
	}
	got, err := Get(ctx, db, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != op.RequestID || got.RequestedBy != op.RequestedBy {
		t.Fatalf("lost correlation: %+v", got)
	}
	got.RequestID, got.RequestedBy = "", ""
	got.State, got.Phase = "running", "installing"
	if err := Save(ctx, db, "sqlite", got); err != nil {
		t.Fatal(err)
	}
	got, err = Get(ctx, db, op.ID)
	if err != nil || got.RequestID != op.RequestID || got.RequestedBy != op.RequestedBy {
		t.Fatalf("background update lost correlation: %+v, %v", got, err)
	}
	got.State = "rolled_back"
	got.Phase = "rolled_back"
	if err := Save(ctx, db, "sqlite", got); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operation_events WHERE operation_id=? AND request_id=? AND requested_by=?`, op.ID, op.RequestID, op.RequestedBy).Scan(&count); err != nil || count != 3 {
		t.Fatalf("transition audit lost actor or request: count=%d, err=%v", count, err)
	}
	if err := CreateExclusive(ctx, db, "sqlite", other); err != nil {
		t.Fatalf("terminal lock not released: %v", err)
	}
	got.State, got.Phase = "running", "installing"
	if err := Save(ctx, db, "sqlite", got); err == nil {
		t.Fatal("terminal operation reopened")
	}
	other.TargetID = "8"
	if err := CreateExclusive(ctx, db, "sqlite", other); err == nil {
		t.Fatal("operation ID reused for another target")
	}
}
