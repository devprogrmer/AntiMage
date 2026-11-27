package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"github.com/antimage/antimage/internal/app/nodecontroller"
)

type historyWorkerFixture struct {
	now   time.Time
	calls []int64
}

func (f *historyWorkerFixture) List(context.Context, nodecontroller.Request) (nodecontroller.NodeListResult, error) {
	return nodecontroller.NodeListResult{Nodes: []nodecontroller.NodeListItem{
		{ID: 99, Status: "disabled"}, {ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5},
	}}, nil
}

func (f *historyWorkerFixture) NetworkIntelligence(_ context.Context, id int64) (json.RawMessage, error) {
	f.calls = append(f.calls, id)
	if id == 2 {
		return nil, errors.New("offline")
	}
	points := []persistedNetworkPoint{
		{NodeID: id, UserID: 99, At: f.now.Add(-2 * time.Minute)},
		{NodeID: id, UserID: 98, At: f.now.Add(time.Second)},
		{NodeID: id, UserID: 1, At: f.now.Add(-time.Second)},
	}
	for user := int64(1); user <= 10; user++ {
		points = append(points, persistedNetworkPoint{NodeID: id, UserID: user, At: f.now})
	}
	return json.Marshal(map[string]any{"history": map[string]any{"points": points}})
}

func TestNetworkHistoryCollectorRotationAndRestart(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE network_quality_history(node_id BIGINT,user_id BIGINT,observed_at BIGINT,ack_rtt_ms DOUBLE,jitter_ms DOUBLE,retransmission_delta BIGINT,PRIMARY KEY(node_id,user_id,observed_at))`); err != nil {
		t.Fatal(err)
	}
	f := &historyWorkerFixture{now: time.Now().UTC().Truncate(time.Millisecond)}
	run := func(count int) {
		ticks := make(chan time.Time, count)
		for range count {
			ticks <- f.now
		}
		close(ticks)
		runNetworkHistoryCollector(context.Background(), ticks, f, &ni.HistoryRepository{DB: db}, func() time.Time { return f.now })
	}
	run(2)
	if !slices.Equal(f.calls, []int64{1, 2, 3, 4, 5, 1, 2, 3}) {
		t.Fatal(f.calls)
	}
	// Recreate the worker and repository over the same database.
	run(1)
	for node, want := range map[int64]int{1: 10, 2: 0, 3: 10, 4: 8, 5: 8, 99: 0} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM network_quality_history WHERE node_id=?", node).Scan(&count); err != nil || count != want {
			t.Fatalf("node=%d count=%d err=%v", node, count, err)
		}
	}
	var at int64
	if err := db.QueryRow("SELECT observed_at FROM network_quality_history WHERE node_id=1 AND user_id=1").Scan(&at); err != nil || at != f.now.UnixMilli() {
		t.Fatal(at, err)
	}
}
