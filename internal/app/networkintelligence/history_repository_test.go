package networkintelligence

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDurableHistoryConcurrentAdmissionAtCapacity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	now := time.Now().UTC()
	_, err = db.Exec(`CREATE TABLE network_quality_history(node_id BIGINT,user_id BIGINT,observed_at BIGINT,ack_rtt_ms DOUBLE,jitter_ms DOUBLE,retransmission_delta BIGINT,PRIMARY KEY(node_id,user_id,observed_at))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<99999)
		INSERT INTO network_quality_history(node_id,user_id,observed_at) SELECT 1,n,? FROM ids`, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
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
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE network_quality_history(node_id BIGINT,user_id BIGINT,observed_at BIGINT,ack_rtt_ms DOUBLE,jitter_ms DOUBLE,retransmission_delta BIGINT,PRIMARY KEY(node_id,user_id,observed_at))`)
	if err != nil {
		t.Fatal(err)
	}
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
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	p.UserID = 0
	if r.Add(ctx, p, p.ObservedAt) == nil {
		t.Fatal("accepted invalid user")
	}
}
