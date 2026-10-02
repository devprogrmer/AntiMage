package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/antimage/antimage/internal/app/nodeagent"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func writeOfflineNodePending(t *testing.T, dir, protocol, id string, value uint64, lastACK string) {
	t.Helper()
	legacy := protocol == "xray" || protocol == "openvpn" || protocol == "wireguard"
	batchKey, sampleID, sampleTag, baselineKey := "batch_id", "user_id", "inbound_tag", "next_baseline"
	if legacy {
		batchKey, sampleID, sampleTag, baselineKey = "BatchID", "UserID", "InboundTag", "NextBaseline"
	}
	pending := map[string]any{
		batchKey:    id,
		"samples":   []any{map[string]any{sampleID: int64(10), sampleTag: "offline-test", "value": value}},
		baselineKey: map[string]uint64{},
	}
	state := map[string]any{"baseline": map[string]uint64{}, "pending": pending, "last_acked_batch_id": lastACK}
	path := filepath.Join(dir, protocol, "usage-state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func offlinePanelRepository(t *testing.T) (*sql.DB, Repository) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "offline-panel.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createUsageTables(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS node_wireguard_usage_reflection (
node_id INTEGER NOT NULL, user_id INTEGER NOT NULL, batch_id TEXT NOT NULL,
updated_at DATETIME NOT NULL, PRIMARY KEY (node_id, user_id)
);
INSERT INTO admins (id, users_usage, lifetime_usage) VALUES (1, 0, 0);
INSERT INTO services (id, used_traffic, lifetime_used_traffic, users_usage, updated_at) VALUES (2, 0, 0, 0, CURRENT_TIMESTAMP);
INSERT INTO admins_services (admin_id, service_id, used_traffic, lifetime_used_traffic, updated_at) VALUES (1, 2, 0, 0, CURRENT_TIMESTAMP);
INSERT INTO users (id, status, used_traffic, data_limit, admin_id, service_id) VALUES (10, 'active', 0, NULL, 1, 2);
INSERT INTO nodes (id, status, uplink, downlink, data_limit, usage_coefficient) VALUES (7, 'connected', 0, 0, NULL, 1);
INSERT INTO system (id, uplink, downlink) VALUES (1, 0, 0);`); err != nil {
		t.Fatal(err)
	}
	return db, NewRepository(db, "sqlite")
}

func stageOfflineNodeBatch(t *testing.T, repo Repository, batch *nodev1.UserUsageBatch, nodeFactor, inboundFactor float64) {
	t.Helper()
	deltas := make([]UserUsageDelta, 0, len(batch.GetStats()))
	for _, sample := range batch.GetStats() {
		userID, onlineOnly, ok := parseUserUsageSampleUID(sample.GetUid())
		if !ok || onlineOnly || sample.GetValue() == 0 {
			continue
		}
		deltas = append(deltas, UserUsageDelta{UserID: userID, Value: int64(sample.GetValue()), InboundCoefficient: inboundFactor})
	}
	if err := repo.StoreCollectedUsage(context.Background(), NodeRow{ID: 7, UsageCoefficient: nodeFactor}, batch.GetBatchId(), deltas, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FlushStagedUsage(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineNodeBatchLostACKPanelDBExactlyOnceAllProtocols(t *testing.T) {
	for _, protocol := range []string{"xray", "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ikev2", "anyconnect"} {
		for _, coefficients := range []struct {
			raw, expected uint64
			node, inbound float64
		}{
			{500 * 1024 * 1024, 500 * 1024 * 1024, 1, 1},
			{100 * 1024 * 1024, 300 * 1024 * 1024, 2, 1.5},
		} {
			t.Run(fmt.Sprintf("%s/%d", protocol, coefficients.raw), func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				db, repo := offlinePanelRepository(t)
				writeOfflineNodePending(t, dir, protocol, protocol+"-offline-1", coefficients.raw, "")
				node := nodeagent.New(nodeagent.Config{DataDir: dir})
				batch, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
				if err != nil || len(batch.GetStats()) != 1 || batch.GetStats()[0].GetValue() != coefficients.raw {
					t.Fatalf("persisted offline batch: %v, %v", batch, err)
				}
				stageOfflineNodeBatch(t, repo, batch, coefficients.node, coefficients.inbound)
				assertInt64(t, db, `SELECT used_traffic FROM users WHERE id = 10`, int64(coefficients.expected))
				// The panel committed but its ACK was lost. Both sides restart.
				node = nodeagent.New(nodeagent.Config{DataDir: dir})
				repo = NewRepository(db, "sqlite")
				retry, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
				if err != nil || retry.GetBatchId() != batch.GetBatchId() || retry.GetStats()[0].GetValue() != coefficients.raw {
					t.Fatalf("lost ACK changed resend: %v, %v", retry, err)
				}
				stageOfflineNodeBatch(t, repo, retry, coefficients.node, coefficients.inbound)
				assertInt64(t, db, `SELECT used_traffic FROM users WHERE id = 10`, int64(coefficients.expected))
				for i := 0; i < 2; i++ {
					ack, err := node.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: retry.GetBatchId()})
					if err != nil || !ack.GetAcknowledged() {
						t.Fatalf("ACK %d: %v, %v", i, ack, err)
					}
				}
				var state struct {
					Pending json.RawMessage `json:"pending"`
					ACK     string          `json:"last_acked_batch_id"`
				}
				raw, err := os.ReadFile(filepath.Join(dir, protocol, "usage-state.json"))
				if err != nil || json.Unmarshal(raw, &state) != nil || (len(state.Pending) > 0 && string(state.Pending) != "null") || state.ACK != protocol+"-offline-1" {
					t.Fatalf("ACK did not durably prune pending: %s, %v", raw, err)
				}
			})
		}
	}
}

func TestCombinedChildACKCrashWindowWithPanelDB(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, repo := offlinePanelRepository(t)
	const mb = uint64(1024 * 1024)
	writeOfflineNodePending(t, dir, "xray", "xray-before-crash", 250*mb, "")
	writeOfflineNodePending(t, dir, "wireguard", "wireguard-before-crash", 250*mb, "")
	node := nodeagent.New(nodeagent.Config{DataDir: dir})
	batch, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || len(batch.GetStats()) != 2 {
		t.Fatalf("combined batch: %v, %v", batch, err)
	}
	stageOfflineNodeBatch(t, repo, batch, 1, 1)
	for _, id := range []string{"xray-before-crash", "wireguard-before-crash"} {
		ack, err := node.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: id})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("child ACK: %v, %v", ack, err)
		}
	}
	// Simulate newer child snapshots after child ACK, with the old combined
	// wrapper still on disk because the process died before its persistence.
	writeOfflineNodePending(t, dir, "xray", "xray-after-crash", 50*mb, "xray-before-crash")
	writeOfflineNodePending(t, dir, "wireguard", "wireguard-after-crash", 50*mb, "wireguard-before-crash")
	node = nodeagent.New(nodeagent.Config{DataDir: dir})
	newer, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
	if err != nil || newer.GetBatchId() == batch.GetBatchId() || len(newer.GetStats()) != 2 {
		t.Fatalf("stale combined wrapper did not recover: %v, %v", newer, err)
	}
	stageOfflineNodeBatch(t, repo, batch, 1, 1)
	stageOfflineNodeBatch(t, repo, newer, 1, 1)
	assertInt64(t, db, `SELECT used_traffic FROM users WHERE id = 10`, int64(600*mb))
	ack, err := node.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: newer.GetBatchId()})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("recovered combined ACK: %v, %v", ack, err)
	}
}
