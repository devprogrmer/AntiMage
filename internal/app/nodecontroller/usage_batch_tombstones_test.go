package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUsageBatchRetryAfterPruneAndRestart(t *testing.T) {
	for _, outbound := range []bool{false, true} {
		name := "user"
		if outbound {
			name = "outbound"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dsn := "file:" + filepath.Join(t.TempDir(), "usage.db") + "?_pragma=busy_timeout(30000)"
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			createUsageTables(t, ctx, db)
			if _, err := db.ExecContext(ctx, `INSERT INTO users (id,status) VALUES (10,'active'); INSERT INTO nodes (id,status) VALUES (7,'connected'); INSERT INTO system (id) VALUES (1)`); err != nil {
				t.Fatal(err)
			}
			const amount int64 = 500 * 1024 * 1024
			repo := NewRepository(db, "sqlite")
			store := func() error {
				if outbound {
					return repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "", nil, "lost-ack", []OutboundUsageDelta{{Tag: "direct", Up: amount}})
				}
				return repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "lost-ack", []UserUsageDelta{{UserID: 10, Value: amount, Online: true}}, "", nil)
			}
			flush := func() {
				if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store(); err != nil {
				t.Fatal(err)
			}
			flush()
			if count, err := repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100); err != nil || count != 1 {
				t.Fatalf("prune = %d, %v", count, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			repo = NewRepository(db, "sqlite")
			if _, err := db.ExecContext(ctx, `DELETE FROM user_presence`); err != nil {
				t.Fatal(err)
			}
			if err := store(); err != nil {
				t.Fatal(err)
			}
			flush()
			if outbound {
				assertInt64(t, db, `SELECT uplink FROM outbound_traffic`, amount)
				assertInt64(t, db, `SELECT uplink FROM node_usages`, amount)
				assertInt64(t, db, `SELECT uplink FROM system`, amount)
			} else {
				assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=10`, amount)
				assertInt64(t, db, `SELECT used_traffic FROM node_user_usages`, amount)
				assertInt64(t, db, `SELECT COUNT(*) FROM user_presence WHERE user_id=10`, 1)
			}
		})
	}
}

func openBatchTombstoneDB(t *testing.T) (*sql.DB, Repository) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "usage.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	createUsageTables(t, context.Background(), db)
	if _, err := db.Exec(`INSERT INTO users (id,status) VALUES (10,'active'),(11,'active'); INSERT INTO nodes (id,status) VALUES (7,'connected'); INSERT INTO system (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	return db, NewRepository(db, "sqlite")
}

func TestUsageBatchTombstonePartialPrunePreservesPendingRows(t *testing.T) {
	db, repo := openBatchTombstoneDB(t)
	ctx := context.Background()
	deltas := []UserUsageDelta{{UserID: 10, Value: 500}, {UserID: 11, Value: 500}}
	store := func() {
		if err := repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "partial", deltas, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	store()
	// Simulate queue rows staged before migration 61 introduced guard rows.
	if _, err := db.Exec(`DELETE FROM node_usage_batch_tombstones`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsageHistory(ctx, 1, UsagePersistOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 1); err != nil || n != 1 {
		t.Fatalf("prune=%d, %v", n, err)
	}
	store()
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_user_queue`, 1)
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
		t.Fatal(err)
	}
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=10`, 500)
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=11`, 500)
	assertInt64(t, db, `SELECT SUM(used_traffic) FROM node_user_usages`, 1000)
}

func TestUsageBatchTombstonePruneRollback(t *testing.T) {
	db, repo := openBatchTombstoneDB(t)
	ctx := context.Background()
	if err := repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "atomic", []UserUsageDelta{{UserID: 10, Value: 500}}, "atomic", []OutboundUsageDelta{{Tag: "direct", Up: 500}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_prune BEFORE DELETE ON node_usage_outbound_queue BEGIN SELECT RAISE(ABORT, 'injected prune failure'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100); err == nil || n != 0 {
		t.Fatalf("prune=%d, %v", n, err)
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_user_queue`, 1)
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_outbound_queue`, 1)
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_batch_tombstones WHERE pruned=1`, 0)
	if _, err := db.Exec(`DROP TRIGGER fail_prune`); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100); err != nil || n != 2 {
		t.Fatalf("prune=%d, %v", n, err)
	}
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_batch_tombstones WHERE pruned=1`, 2)
}

func TestUsageBatchTombstoneKeepsOutboundRetryEnrichment(t *testing.T) {
	db, repo := openBatchTombstoneDB(t)
	ctx := context.Background()
	store := func(up, inbound int64) {
		if err := repo.StoreCollectedUsageWithInbounds(ctx, NodeRow{ID: 7}, "", nil, "enrich", []OutboundUsageDelta{{Tag: "direct", Up: up}}, []InboundUsageDelta{{Tag: "direct", Up: inbound}}); err != nil {
			t.Fatal(err)
		}
	}
	store(500, 0)
	store(999, 200)
	assertInt64(t, db, `SELECT uplink FROM node_usage_outbound_queue`, 500)
	assertInt64(t, db, `SELECT inbound_uplink FROM node_usage_outbound_queue`, 200)
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	store(999, 900)
	assertInt64(t, db, `SELECT uplink FROM node_usage_outbound_queue`, 500)
	assertInt64(t, db, `SELECT inbound_uplink FROM node_usage_outbound_queue`, 200)
}

func TestUsageBatchTombstoneConcurrentPruneRetry(t *testing.T) {
	db, repo := openBatchTombstoneDB(t)
	ctx := context.Background()
	store := func() error {
		return repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "concurrent", []UserUsageDelta{{UserID: 10, Value: 500}}, "concurrent", []OutboundUsageDelta{{Tag: "direct", Up: 500}})
	}
	if err := store(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM node_usage_batch_tombstones`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(prune bool) {
			defer wg.Done()
			<-start
			var err error
			if prune {
				_, err = repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100)
			} else {
				err = store()
			}
			errs <- err
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatal(err)
		}
	}
	// Retry transactions rejected by SQLite's lock upgrade; successful calls must
	// already have preserved the guard and queue atomically.
	if _, err := repo.PruneProcessedUsageQueue(ctx, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if err := store(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=10`, 500)
	assertInt64(t, db, `SELECT uplink FROM outbound_traffic`, 500)
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_user_queue`, 0)
	assertInt64(t, db, `SELECT COUNT(*) FROM node_usage_outbound_queue`, 0)
}

func TestUsageBatchTombstoneIdentityIsolation(t *testing.T) {
	db, repo := openBatchTombstoneDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO nodes (id,status) VALUES (8,'connected'); INSERT INTO node_usage_batch_tombstones (node_id,batch_id,kind,pruned) VALUES (7,'same','user',1)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "same", []UserUsageDelta{{UserID: 10, Value: 500}}, "same", []OutboundUsageDelta{{Tag: "direct", Up: 500}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreCollectedUsage(ctx, NodeRow{ID: 8}, "same", []UserUsageDelta{{UserID: 10, Value: 500}}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.StoreCollectedUsage(ctx, NodeRow{ID: 7}, "different", []UserUsageDelta{{UserID: 10, Value: 500}}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
		t.Fatal(err)
	}
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=10`, 1000)
	assertInt64(t, db, `SELECT uplink FROM outbound_traffic WHERE target_id='node:7'`, 500)
}
