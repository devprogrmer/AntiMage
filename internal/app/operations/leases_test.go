package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func leaseTestDatabases(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "leases.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return db
	}
	a, b := open(), open()
	for _, ddl := range []string{ExecutorLeaseDDL,
		ResourceFenceDDL,
		`CREATE TABLE operations(id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`,
		`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
		`CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
	} {
		if _, err := a.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func seedLeaseOperation(t *testing.T, db *sql.DB, phase string) Operation {
	t.Helper()
	op := Operation{ID: "operation", Type: "node_update", TargetType: "node", TargetID: "7", RequestedBy: "owner", RequestID: "request-original", State: "running", Phase: phase, CreatedAt: 1, UpdatedAt: 1, Metadata: map[string]any{"reconnect_deadline": 12345}}
	if err := CreateExclusive(context.Background(), db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestExecutorLeaseConcurrentControllersOneWinner(t *testing.T) {
	for _, phase := range []string{"downloading", "installing", "rolling_back", "preflight", "restoring"} {
		t.Run(phase, func(t *testing.T) {
			a, b := leaseTestDatabases(t)
			seedLeaseOperation(t, a, phase)
			start := make(chan struct{})
			results := make(chan error, 2)
			var group sync.WaitGroup
			for index, db := range []*sql.DB{a, b} {
				group.Add(1)
				go func(index int, db *sql.DB) {
					defer group.Done()
					<-start
					executor := []string{"controller-a", "controller-b"}[index]
					_, err := AcquireExecutorLease(context.Background(), db, "sqlite", "operation", executor, time.Minute)
					results <- err
				}(index, db)
			}
			close(start)
			group.Wait()
			close(results)
			winners, blocked := 0, 0
			for err := range results {
				if err == nil {
					winners++
				} else if errors.Is(err, ErrLeaseHeld) {
					blocked++
				} else {
					t.Fatal(err)
				}
			}
			if winners != 1 || blocked != 1 {
				t.Fatalf("winners=%d blocked=%d", winners, blocked)
			}
		})
	}
}

func TestExecutorLeaseCrashTakeoverFencesOldWrites(t *testing.T) {
	a, b := leaseTestDatabases(t)
	op := seedLeaseOperation(t, a, "installing")
	ctx := context.Background()
	first, err := AcquireExecutorLease(ctx, a, "sqlite", op.ID, "controller-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(`UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireExecutorLease(ctx, b, "sqlite", op.ID, "controller-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("generation did not advance: %+v %+v", first, second)
	}
	op.Phase = "completed"
	op.State = "completed"
	if err := Save(WithExecutorLease(ctx, first), a, "sqlite", op); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale executor persisted success: %v", err)
	}
	if err := RenewExecutorLease(ctx, a, first, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale renewal accepted: %v", err)
	}
	if err := ReleaseExecutorLease(ctx, a, first); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old owner released new lease: %v", err)
	}
	if err := AuthorizeNodeMutation(ctx, a, op.ID); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("unleased mutation accepted: %v", err)
	}
	if err := Save(WithNodeMutation(ctx, op.ID), a, "sqlite", op); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("unleased scheduler bypassed transactional fencing: %v", err)
	}
	if err := RenewExecutorLease(ctx, b, second, time.Minute); err != nil {
		t.Fatal(err)
	}
	stored, err := Get(ctx, b, op.ID)
	if err != nil || stored.Metadata["reconnect_deadline"].(interface{ String() string }).String() != "12345" {
		t.Fatalf("lease renewal changed business deadline: %+v %v", stored, err)
	}
	if err := Save(WithExecutorLease(ctx, second), b, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	var locks int
	if err := b.QueryRow(`SELECT COUNT(*) FROM operation_locks`).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("terminal resource lock remains: %d %v", locks, err)
	}
	if err := ReleaseExecutorLease(ctx, b, second); err != nil {
		t.Fatal(err)
	}
}

func TestResourceGenerationMonotonicAcrossDifferentOperations(t *testing.T) {
	for _, pair := range [][2]string{{"node_update", "node_rollback"}, {"node_rollback", "node_update"}} {
		t.Run(pair[0]+"_to_"+pair[1], func(t *testing.T) {
			a, b := leaseTestDatabases(t)
			ctx := context.Background()
			firstOp := seedLeaseOperation(t, a, "installing")
			firstOp.Type = pair[0]
			// Set the fixture's intended operation type before execution.
			if _, err := a.Exec(`UPDATE operations SET operation_type=? WHERE id=?`, pair[0], firstOp.ID); err != nil {
				t.Fatal(err)
			}
			first, err := AcquireExecutorLease(ctx, a, "sqlite", firstOp.ID, "controller-a", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			firstOp.State, firstOp.Phase = "failed", "failed"
			if err := Save(WithExecutorLease(ctx, first), a, "sqlite", firstOp); err != nil {
				t.Fatal(err)
			}
			secondOp := firstOp
			secondOp.ID = "operation-b"
			secondOp.Type = pair[1]
			secondOp.State, secondOp.Phase = "running", "preflight"
			if err := CreateExclusive(ctx, b, "sqlite", secondOp); err != nil {
				t.Fatal(err)
			}
			second, err := AcquireExecutorLease(ctx, b, "sqlite", secondOp.ID, "controller-b", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if second.Generation != 1 || second.ResourceGeneration != first.ResourceGeneration+1 {
				t.Fatalf("resource fence reset with new operation: %+v %+v", first, second)
			}
			if err := CheckExecutorLease(WithExecutorLease(ctx, first), a, firstOp.ID); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("old operation still owns target: %v", err)
			}
			if err := RenewExecutorLease(ctx, a, first, time.Minute); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("superseded operation renewed: %v", err)
			}
			if err := Save(WithExecutorLease(ctx, first), a, "sqlite", firstOp); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale cross-operation write accepted: %v", err)
			}
			if err := CheckExecutorLease(WithExecutorLease(ctx, second), b, secondOp.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
