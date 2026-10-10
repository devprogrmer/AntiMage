package nodecontroller

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
)

func rolloutTestController(t *testing.T) Controller {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "rollouts.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, ddl := range []string{
		operationapp.ExecutorLeaseDDL,
		operationapp.ResourceFenceDDL,
		`CREATE TABLE nodes(id INTEGER PRIMARY KEY,node_binary_tag TEXT,node_capabilities TEXT DEFAULT '["shared_fencing_v1","command_idempotency_v1"]');INSERT INTO nodes(id) VALUES(1),(2),(3),(4),(5)`,
		`CREATE TABLE node_operations(id INTEGER PRIMARY KEY,operation_type TEXT,node_id INTEGER,status TEXT)`,
		`CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,requested_by TEXT NOT NULL,request_id TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,progress INTEGER,created_at BIGINT NOT NULL,started_at BIGINT,updated_at BIGINT NOT NULL,completed_at BIGINT,error TEXT NOT NULL,metadata_json TEXT NOT NULL)`,
		`CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
		`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return NewController(NewRepository(db, "sqlite"))
}
func testRolloutTarget() systemapp.ResolvedInstall {
	return systemapp.ResolvedInstall{BuildCatalogEntry: systemapp.BuildCatalogEntry{Version: "dev-abcdef1", Channel: "dev", Commit: "abcdef1" + strings.Repeat("0", 33), Architecture: "amd64", OS: "linux", SHA256: strings.Repeat("a", 64), Size: 42, DownloadURL: "https://example.test/node", ArtifactName: "node"}, RequestedChannel: "dev", RequestedPolicy: "latest"}
}
func waitRollout(t *testing.T, c Controller, id string) RolloutView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, err := c.Rollout(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if operationapp.Terminal(v.Operation.State) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rollout did not terminate")
	return RolloutView{}
}

func TestRolloutBoundedConcurrencyAndImmutableChildren(t *testing.T) {
	c := rolloutTestController(t)
	var active, maximum atomic.Int32
	c.rolloutApply = func(ctx context.Context, req Request) (RuntimeResult, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if count <= old || maximum.CompareAndSwap(old, count) {
				break
			}
		}
		op, err := c.repo.NodeUpdateOperation(ctx, req.OperationID)
		if err != nil {
			return RuntimeResult{}, err
		}
		if op.ResolvedTarget == nil || op.ResolvedTarget.Version != "dev-abcdef1" {
			return RuntimeResult{}, fmt.Errorf("immutable target missing")
		}
		time.Sleep(25 * time.Millisecond)
		return RuntimeResult{}, c.repo.AdvanceNodeUpdate(ctx, op.ID, "completed", 100, "", op.DesiredVersion, op.DesiredVersion, "", "", true)
	}
	view, err := c.persistRollout(context.Background(), RolloutRequest{Confirm: true, NodeIDs: []int64{1, 2, 3, 4, 5}, Mode: "bulk", Concurrency: 2}, testRolloutTarget(), map[int64]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitRollout(t, c, view.Rollout.ID)
	if maximum.Load() > 2 || maximum.Load() < 2 || final.Summary["completed"] != 5 || final.Operation.State != "completed" {
		t.Fatalf("maximum=%d result=%+v", maximum.Load(), final)
	}
}

func TestCanaryFailureStopsRemainingAndRetryOnlyFailed(t *testing.T) {
	c := rolloutTestController(t)
	var mu sync.Mutex
	started := []int64{}
	c.rolloutApply = func(ctx context.Context, req Request) (RuntimeResult, error) {
		mu.Lock()
		started = append(started, req.NodeID)
		mu.Unlock()
		return RuntimeResult{}, fmt.Errorf("canary health verification failed")
	}
	view, err := c.persistRollout(context.Background(), RolloutRequest{Confirm: true, NodeIDs: []int64{1, 2, 3, 4, 5}, Mode: "canary", CanaryCount: 1, Concurrency: 2}, testRolloutTarget(), map[int64]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitRollout(t, c, view.Rollout.ID)
	mu.Lock()
	calls := append([]int64{}, started...)
	mu.Unlock()
	if len(calls) != 1 || calls[0] != 1 || final.Operation.Phase != "canary_failed" || final.Summary["cancelled"] != 4 {
		t.Fatalf("started=%v result=%+v", calls, final)
	}
	retry, err := c.RetryRollout(context.Background(), view.Rollout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Children) != 1 || retry.Children[0].NodeID != 1 || retry.Children[0].ID == view.Children[0].ID || retry.Rollout.RetryOf != view.Rollout.ID || retry.Rollout.ResolvedTarget.Commit != view.Rollout.ResolvedTarget.Commit {
		t.Fatalf("incorrect failed-only retry: %+v", retry)
	}
	waitRollout(t, c, retry.Rollout.ID)
}

func TestCanarySuccessReleasesRemainingNodes(t *testing.T) {
	c := rolloutTestController(t)
	var passed atomic.Bool
	c.rolloutApply = func(ctx context.Context, req Request) (RuntimeResult, error) {
		if req.NodeID != 1 && !passed.Load() {
			return RuntimeResult{}, fmt.Errorf("non-canary released before verification")
		}
		if req.NodeID == 1 {
			time.Sleep(25 * time.Millisecond)
			passed.Store(true)
		}
		return RuntimeResult{}, c.repo.AdvanceNodeUpdate(ctx, req.OperationID, "completed", 100, "", "dev-abcdef1", "dev-abcdef1", "", "", true)
	}
	view, err := c.persistRollout(context.Background(), RolloutRequest{Confirm: true, NodeIDs: []int64{1, 2, 3, 4, 5}, Mode: "canary", CanaryCount: 1, Concurrency: 2}, testRolloutTarget(), map[int64]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitRollout(t, c, view.Rollout.ID)
	if final.Summary["completed"] != 5 {
		t.Fatalf("canary release failed: %+v", final)
	}
}

func TestUnconfirmedRolloutSurvivesReloadAndCancellationReleasesLocks(t *testing.T) {
	c := rolloutTestController(t)
	var called atomic.Bool
	c.rolloutApply = func(context.Context, Request) (RuntimeResult, error) {
		called.Store(true)
		return RuntimeResult{}, fmt.Errorf("unconfirmed execution")
	}
	view, err := c.persistRollout(context.Background(), RolloutRequest{NodeIDs: []int64{1, 2}, Mode: "bulk", Concurrency: 2}, testRolloutTarget(), map[int64]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewController(NewRepository(c.repo.db, "sqlite"))
	if err := reopened.RecoverRollouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Rollout(context.Background(), view.Rollout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Rollout.Confirmed || loaded.Operation.Phase != "awaiting_confirmation" || len(loaded.Children) != 2 || loaded.Children[0].RolloutID != view.Rollout.ID {
		t.Fatalf("draft lost across reload: %+v", loaded)
	}
	cancelled, err := c.CancelRollout(context.Background(), view.Rollout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Operation.State != "cancelled" || cancelled.Summary["cancelled"] != 2 || called.Load() {
		t.Fatalf("draft executed or cancellation failed: %+v", cancelled)
	}
	var locks int
	if err := c.repo.db.QueryRow("SELECT COUNT(*) FROM operation_locks").Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("locks remain: %d %v", locks, err)
	}
	if _, err := c.StartRollout(context.Background(), view.Rollout.ID); err == nil {
		t.Fatal("cancelled rollout restarted")
	}
}
