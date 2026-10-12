package nodecontroller

import (
	"context"
	"database/sql"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"path/filepath"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/platform/requestctx"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	_ "modernc.org/sqlite"
)

func TestNodeUpdateRuntimeMatchRejectsOldOrUnhealthyState(t *testing.T) {
	began := time.Now().Add(-time.Minute)
	healthy := nodev1.RuntimeState{Connected: true, Started: true, NodeVersion: "dev-abcdef1", ProcessStartedAtUnixNano: began.Add(time.Second).UnixNano(), SampledAtUnixNano: began.Add(2 * time.Second).UnixNano()}
	if !nodeUpdateRuntimeMatches(&healthy, "dev-abcdef1", began) {
		t.Fatal("fresh healthy restarted runtime rejected")
	}
	for _, mutate := range []func(*nodev1.RuntimeState){
		func(s *nodev1.RuntimeState) { s.NodeVersion = "dev-old" },
		func(s *nodev1.RuntimeState) { s.Started = false },
		func(s *nodev1.RuntimeState) { s.Connected = false },
		func(s *nodev1.RuntimeState) { s.ProcessStartedAtUnixNano = began.UnixNano() },
		func(s *nodev1.RuntimeState) { s.ProcessStartedAtUnixNano = 0; s.SampledAtUnixNano = 0 },
		func(s *nodev1.RuntimeState) { s.SampledAtUnixNano = began.UnixNano() },
	} {
		state := healthy
		mutate(&state)
		if nodeUpdateRuntimeMatches(&state, "dev-abcdef1", began) {
			t.Fatalf("accepted stale or unhealthy runtime: %+v", state)
		}
	}
}

func TestNodeVersionMatchesRequestedDevShortOrFullSHA(t *testing.T) {
	if !nodeVersionMatchesRequested("dev-abcdef0123456789", "dev-abcdef0") {
		t.Fatal("expected pinned short SHA to match the node-reported full SHA")
	}
	if nodeVersionMatchesRequested("dev-abcdef1", "dev-abcdef0") {
		t.Fatal("different commit prefix must not match")
	}
	if nodeVersionMatchesRequested("v1.2.4", "v1.2.3") {
		t.Fatal("different stable version must not match")
	}
}

func TestNodeUpdatePreflightRequiresRuntimeEvidence(t *testing.T) {
	if err := requireNodeUpdateEvidence(&nodev1.RuntimeState{Connected: true, NodeVersion: "v1.2.3"}); err == nil {
		t.Fatal("old node accepted without process evidence")
	}
	state := &nodev1.RuntimeState{ProcessStartedAtUnixNano: 10, SampledAtUnixNano: 11}
	if err := requireNodeUpdateEvidence(state); err == nil {
		t.Fatal("unsupported node accepted")
	}
	state.Capabilities = []string{"runtime_evidence_v1"}
	if err := requireNodeUpdateEvidence(state); err == nil {
		t.Fatal("agent accepted without verified installer capability")
	}
	state.Capabilities = append(state.Capabilities, "verified_updates_v1")
	if err := requireNodeUpdateEvidence(state); err == nil {
		t.Fatal("verified update accepted without shared fencing")
	}
	state.Capabilities = append(state.Capabilities, "shared_fencing_v1", "command_idempotency_v1")
	if err := requireNodeUpdateEvidence(state); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNodeServiceUpdateTargetRequiresExactChannelBuild(t *testing.T) {
	tests := []struct {
		name, channel, version, wantChannel string
		wantErr                             bool
	}{
		{name: "stable exact", channel: "stable", version: "v1.2.3", wantChannel: "stable"},
		{name: "stable downgrade", channel: "latest", version: "v1.1.0", wantChannel: "stable"},
		{name: "dev exact", channel: "dev", version: "dev-abcdef0123456789", wantChannel: "dev"},
		{name: "stable latest rejected", channel: "stable", version: "latest", wantErr: true},
		{name: "dev latest rejected", channel: "dev", version: "", wantErr: true},
		{name: "channel mismatch rejected", channel: "stable", version: "dev-abcdef0", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotChannel, gotVersion, err := validateNodeServiceUpdateTarget(test.channel, test.version)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected exact-target validation error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotChannel != test.wantChannel || gotVersion != test.version {
				t.Fatalf("target = %q/%q, want %q/%q", gotChannel, gotVersion, test.wantChannel, test.version)
			}
		})
	}
}

func TestNodeUpdateOperationsPersistStateAndRejectConflicts(t *testing.T) {
	ctx := requestctx.WithAdmin(requestctx.WithID(context.Background(), "req-node-update-0001"), "operator")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "node-updates.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE nodes (id INTEGER PRIMARY KEY, node_binary_tag TEXT); INSERT INTO nodes (id) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE node_operations (
id INTEGER PRIMARY KEY, operation_type TEXT NOT NULL, node_id INTEGER, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE operations (
id TEXT PRIMARY KEY, operation_type TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL,
requested_by TEXT NOT NULL, request_id TEXT NOT NULL, state TEXT NOT NULL, phase TEXT NOT NULL,
progress INTEGER, created_at BIGINT NOT NULL, started_at BIGINT, updated_at BIGINT NOT NULL,
completed_at BIGINT, error TEXT NOT NULL, metadata_json TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, operationapp.ExecutorLeaseDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, operationapp.ResourceFenceDDL); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	started := time.Now().UTC().Truncate(time.Second)
	op := NodeUpdateOperation{ID: "update_service-42", NodeID: 7, RequestedChannel: "dev", UpdatePolicy: "pinned", RequestedVersion: "dev-abcdef1", PreviousVersion: "dev-1234567", DesiredVersion: "dev-abcdef1", Phase: "queued", StartedAt: started}
	if err := repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	var requestID, requestedBy string
	if err := db.QueryRowContext(ctx, `SELECT request_id, requested_by FROM operations WHERE id=?`, op.ID).Scan(&requestID, &requestedBy); err != nil {
		t.Fatal(err)
	}
	if requestID != "req-node-update-0001" || requestedBy != "operator" {
		t.Fatalf("operation correlation = %q/%q", requestID, requestedBy)
	}
	if err := repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "update_service-43", NodeID: 7, RequestedChannel: "stable", RequestedVersion: "latest"}); err == nil {
		t.Fatal("concurrent update was not rejected")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO node_operations (id, operation_type, node_id, status) VALUES (4, 'restart_service', 7, 'running')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceNodeUpdate(ctx, op.ID, "failed", 100, "", "", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	if err := repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "update_service-44", NodeID: 7, RequestedChannel: "stable", RequestedVersion: "v1.2.3"}); err == nil {
		t.Fatal("node update was accepted while a conflicting service restart was active")
	}
	if _, err := db.ExecContext(ctx, `UPDATE node_operations SET status = 'done' WHERE id = 4`); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceNodeUpdate(ctx, op.ID, "installing", 50, "", "", "", "", "", false); err == nil {
		t.Fatal("terminal operation was reopened")
	}
	op.ID = "update_service-45"
	op.StartedAt = started.Add(time.Second)
	if err := repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatalf("could not create update after clearing conflict: %v", err)
	}
	if err := repo.AdvanceNodeUpdate(ctx, op.ID, "waiting_for_reconnect", 85, "dev-abcdef1234567", "dev-abcdef1234567", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceNodeUpdate(ctx, op.ID, "completed", 100, "dev-abcdef1234567", "dev-abcdef1234567", "dev-abcdef1234567", "", "", true); err != nil {
		t.Fatal(err)
	}
	got, err := repo.LatestNodeUpdate(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "completed" || got.Progress != 100 || got.RequestedVersion != "dev-abcdef1" || got.ResolvedVersion != "dev-abcdef1234567" || got.InstalledVersion != got.ResolvedVersion || got.RunningVersion != got.ResolvedVersion || got.CompletedAt == nil {
		t.Fatalf("unexpected persisted update operation: %#v", got)
	}
	history, err := repo.NodeUpdateHistory(ctx, 7, 10)
	if err != nil || len(history) != 2 || history[0].ID != op.ID {
		t.Fatalf("update history = %#v, err=%v", history, err)
	}
}
