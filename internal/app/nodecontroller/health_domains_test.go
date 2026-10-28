package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	_ "modernc.org/sqlite"
)

func TestRuntimeResultKeepsWorkerDegradationSeparateFromConnectivity(t *testing.T) {
	state := &nodev1.RuntimeState{
		Connected:    true,
		Capabilities: []string{"accounting_degraded"},
	}
	result := runtimeResult(NodeRow{ID: 9, Status: "connected"}, state, nil)
	if result.Status != "connected" || !result.Connected {
		t.Fatalf("worker degradation changed connectivity: status=%q connected=%v", result.Status, result.Connected)
	}
	if result.AgentStatus != "degraded" {
		t.Fatalf("agent status=%q, want degraded", result.AgentStatus)
	}
}

func TestSetRuntimeStatePersistsWorkerDegradationWithoutChangingNodeConnection(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "health-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT, agent_status TEXT, xray_status TEXT, applied_revision INTEGER, node_capabilities TEXT, last_seen_at DATETIME); INSERT INTO nodes(id,status,agent_status) VALUES(1,'connected','connected')`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	if err := repo.SetRuntimeState(ctx, 1, &nodev1.RuntimeState{Capabilities: []string{"accounting_degraded"}}); err != nil {
		t.Fatal(err)
	}
	assertRepositoryString(t, db, `SELECT status FROM nodes WHERE id=1`, "connected")
	assertRepositoryString(t, db, `SELECT agent_status FROM nodes WHERE id=1`, "degraded")
	if err := repo.SetRuntimeState(ctx, 1, &nodev1.RuntimeState{}); err != nil {
		t.Fatal(err)
	}
	assertRepositoryString(t, db, `SELECT agent_status FROM nodes WHERE id=1`, "connected")
}
