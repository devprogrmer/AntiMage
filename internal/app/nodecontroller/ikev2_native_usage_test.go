package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"testing"
)

// This repository-level test consumes only batches emitted by the live native
// collector. It does not claim HTTP/gRPC or browser coverage.
func TestIKEv2NativePanelDB(t *testing.T) {
	dir := os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")
	if dir == "" {
		t.Skip("requires real native collector batches")
	}
	ctx := context.Background()
	batches := []*nodev1.UserUsageBatch{}
	var rawTotal uint64
	ids := []string{}
	for i, name := range []string{"native-first-batch.pb", "native-next-batch.pb"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if i == 1 && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		batch := &nodev1.UserUsageBatch{}
		if err := proto.Unmarshal(raw, batch); err != nil {
			t.Fatal(err)
		}
		if batch.BatchId == "" {
			t.Fatal("empty native batch")
		}
		batches = append(batches, batch)
		ids = append(ids, batch.BatchId)
		for _, sample := range batch.Stats {
			uid, online, ok := parseUserUsageSampleUID(sample.Uid)
			if !ok || uid != 7 {
				t.Fatalf("unexpected native UID: %q", sample.Uid)
			}
			if !online {
				rawTotal += sample.Value
			}
		}
	}
	path := filepath.Join(dir, "native-panel.db")
	_, statErr := os.Stat(path)
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if os.IsNotExist(statErr) {
		createUsageTables(t, ctx, db)
		_, err := db.ExecContext(ctx, `INSERT INTO admins (id) VALUES (1);
INSERT INTO services (id) VALUES (2);
INSERT INTO admins_services (admin_id,service_id) VALUES (1,2);
INSERT INTO users (id,status,data_limit,admin_id,service_id) VALUES (7,'active',52428800,1,2);
INSERT INTO nodes (id,status,usage_coefficient) VALUES (9,'connected',1.5);
INSERT INTO inbounds (tag) VALUES ('native');
INSERT INTO system (id) VALUES (1);`)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() { db.Close() }()
	for attempt := 0; attempt < 3; attempt++ {
		repo := NewRepository(db, "sqlite")
		for _, batch := range batches {
			deltas := []UserUsageDelta{}
			for _, sample := range batch.Stats {
				uid, online, ok := parseUserUsageSampleUID(sample.Uid)
				if !ok {
					t.Fatal("invalid collector UID")
				}
				value := int64(sample.Value)
				if online {
					value = 0
				}
				deltas = append(deltas, UserUsageDelta{UserID: uid, Value: value, Online: online, InboundCoefficient: 2})
			}
			if err := repo.StoreCollectedUsageWithInbounds(ctx, NodeRow{ID: 9, UsageCoefficient: 1.5}, batch.BatchId, deltas, "", nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.FlushStagedUsage(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.FlushStagedUsageHistory(ctx, 100, UsagePersistOptions{}); err != nil {
			t.Fatal(err)
		}
		assertInt64(t, db, `SELECT used_traffic FROM users WHERE id=7`, int64(rawTotal*3))
		assertInt64(t, db, `SELECT SUM(used_traffic) FROM node_user_usages WHERE user_id=7 AND node_id=9`, int64(rawTotal*3))
		assertInt64(t, db, `SELECT users_usage FROM admins WHERE id=1`, int64(rawTotal*3))
		if rawTotal*3 >= 50<<20 {
			assertString(t, db, `SELECT status FROM users WHERE id=7`, "limited")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open() // Real connection restart; committed batches replay without ACK.
	}
	receipt, err := json.Marshal(map[string]any{"batch_ids": ids, "raw_total": rawTotal, "effective_total": rawTotal * 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native-panel-receipt.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("native DB exact once after replay/reopen: raw=%d effective=%d batches=%v", rawTotal, rawTotal*3, ids)
}
