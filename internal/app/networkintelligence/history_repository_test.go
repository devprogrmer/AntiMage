package networkintelligence

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	database "github.com/antimage/antimage/internal/platform/db"
	_ "modernc.org/sqlite"
)

func openDurableHistoryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if url := os.Getenv("ANTIMAGE_HISTORY_TEST_DATABASE_URL"); url != "" {
		pool, err := database.Open(url)
		if err != nil {
			t.Fatal(err)
		}
		// Open caches pools by URL. Both external cases share this process-owned pool.
		var count int
		if err := pool.DB.QueryRow("SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil || count != 0 {
			t.Fatalf("expected empty migrated test database: rows=%d error=%v", count, err)
		}
		t.Cleanup(func() {
			if _, err := pool.DB.Exec("DELETE FROM network_quality_history WHERE node_id IN (1,2,7)"); err != nil {
				t.Error(err)
			}
		})
		return pool.DB
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE network_quality_history(node_id BIGINT,user_id BIGINT,observed_at BIGINT,ack_rtt_ms DOUBLE,jitter_ms DOUBLE,retransmission_delta BIGINT,PRIMARY KEY(node_id,user_id,observed_at))`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDurableHistoryConcurrentAdmissionAtCapacity(t *testing.T) {
	db := openDurableHistoryTestDB(t)
	now := time.Now().UTC()
	if _, err := db.Exec("INSERT INTO network_quality_history (node_id,user_id,observed_at) VALUES (1,1,?)", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for seeded := 1; seeded < 99999; {
		add := min(seeded, 99999-seeded)
		if _, err := db.Exec(`INSERT INTO network_quality_history (node_id,user_id,observed_at)
			SELECT node_id,user_id+?,observed_at FROM network_quality_history WHERE node_id=1 AND user_id<=?`, seeded, add); err != nil {
			t.Fatal(err)
		}
		seeded += add
	}
	repo := &HistoryRepository{DB: db}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for i := range 4 {
		wg.Go(func() {
			results <- repo.Add(context.Background(), DurableQualityPoint{NodeID: 2, UserID: int64(i + 1), ObservedAt: now}, now)
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if err.Error() != "quality history capacity reached" {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil || count != 100000 || success != 1 {
		t.Fatal(count, success, err)
	}
	if err := repo.Prune(context.Background(), now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestDurableHistoryRetentionIdempotencyAndScope(t *testing.T) {
	db := openDurableHistoryTestDB(t)
	r := &HistoryRepository{DB: db}
	now := time.Now().UTC().Truncate(time.Millisecond)
	rtt := 123.5
	p := DurableQualityPoint{NodeID: 7, UserID: 42, ObservedAt: now, ACKRTTMilliseconds: &rtt}
	ctx := context.Background()
	for range 2 {
		if err := r.Add(ctx, p, now); err != nil {
			t.Fatal(err)
		}
	}
	points, err := r.List(ctx, 7, 42, now.Add(-time.Hour))
	if err != nil || len(points) != 1 || points[0].ACKRTTMilliseconds == nil || *points[0].ACKRTTMilliseconds != rtt {
		t.Fatal(points, err)
	}
	points, err = r.List(ctx, 7, 43, now.Add(-time.Hour))
	if err != nil || len(points) != 0 {
		t.Fatal(points, err)
	}
	if _, err := db.Exec("INSERT INTO network_quality_history(node_id,user_id,observed_at) VALUES(7,42,?)", now.Add(-8*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	p.ObservedAt = now.Add(time.Second)
	if err := r.Add(ctx, p, p.ObservedAt); err != nil {
		t.Fatal(err)
	}
	points, err = (&HistoryRepository{DB: db}).List(ctx, 7, 42, now.Add(-time.Hour))
	if err != nil || len(points) != 2 || !points[0].ObservedAt.Equal(p.ObservedAt) || !points[1].ObservedAt.Equal(now) {
		t.Fatalf("history ordering after repository restart: %v, %v", points, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	p.UserID = 0
	if r.Add(ctx, p, p.ObservedAt) == nil {
		t.Fatal("accepted invalid user")
	}
}
