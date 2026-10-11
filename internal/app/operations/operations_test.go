package operations

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestStateForPhaseNormalizesFeaturePhases(t *testing.T) {
	for phase, want := range map[string]string{"queued": "queued", "waiting_for_reconnect": "waiting", "installing": "running", "completed": "completed", "failed": "failed", "rolled_back": "rolled_back"} {
		if got := StateForPhase(phase); got != want {
			t.Errorf("StateForPhase(%q)=%q, want %q", phase, got, want)
		}
	}
}

func TestOperationSurvivesDatabaseRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.sqlite")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if _, err := db.Exec(`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	progress := 42
	started := int64(101)
	want := Operation{ID: "op-restart", Type: "panel_update", TargetType: "panel", TargetID: "local", RequestedBy: "owner", RequestID: "request-restart", State: "running", Phase: "installing", Progress: &progress, CreatedAt: 100, StartedAt: &started, UpdatedAt: 102, Metadata: map[string]any{"channel": "stable"}}
	if err := Upsert(context.Background(), db, "sqlite", want); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	got, err := List(context.Background(), db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != want.ID || got[0].RequestID != want.RequestID || got[0].State != want.State || got[0].Phase != want.Phase || got[0].Progress == nil || *got[0].Progress != progress {
		t.Fatalf("operation after reopen=%+v", got)
	}
}

func TestUpsertPersistsAndUpdatesGenericOperation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	progress := 50
	op := Operation{ID: "op-1", Type: "node_update", TargetType: "node", TargetID: "7", State: "running", Phase: "installing", Progress: &progress, CreatedAt: 1, UpdatedAt: 2, Metadata: map[string]any{"channel": "stable"}}
	if err := Upsert(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	op.State, op.Phase, op.UpdatedAt = "completed", "completed", 3
	if err := Upsert(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	items, err := List(ctx, db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != "completed" || items[0].Phase != "completed" {
		t.Fatalf("persisted operations=%+v", items)
	}
}
