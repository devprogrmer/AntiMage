package nodecontroller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/app/nodeagent"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

// The native driver supplies unchanged accounting directories produced by real
// WireGuard and OpenVPN collectors. No pending batches or counters are seeded.
func TestNativeVPNCombinedPanelDBLostACK(t *testing.T) {
	dir := os.Getenv("ANTIMAGE_VPN_NATIVE_STATE")
	if dir == "" {
		t.Skip("requires native VPN driver accounting state")
	}
	ctx := context.Background()
	db, repo := offlinePanelRepository(t)
	if _, err := db.ExecContext(ctx, "UPDATE users SET id = 7 WHERE id = 10"); err != nil {
		t.Fatal(err)
	}
	var rawTotal uint64
	seen := map[string]bool{}
	protocols := map[string]bool{}
	for iteration := 0; iteration < 10; iteration++ {
		node := nodeagent.New(nodeagent.Config{DataDir: dir})
		batch, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 && !strings.HasPrefix(batch.GetBatchId(), "combined-") && !strings.HasPrefix(batch.GetBatchId(), "merged-") {
			t.Fatalf("native WireGuard/OpenVPN children did not produce a merged parent batch: %q", batch.GetBatchId())
		}
		var raw uint64
		for _, sample := range batch.GetStats() {
			uid, online, ok := parseUserUsageSampleUID(sample.GetUid())
			if !ok || uid != 7 {
				t.Fatalf("unexpected native sample: %v", sample)
			}
			if online {
				continue
			}
			raw += sample.GetValue()
			if sample.GetValue() > 0 {
				protocols[strings.SplitN(sample.GetUid(), ":", 2)[0]] = true
			}
		}
		if raw == 0 {
			break
		}
		if seen[batch.GetBatchId()] {
			t.Fatalf("ACK did not advance batch %s", batch.GetBatchId())
		}
		seen[batch.GetBatchId()] = true
		rawTotal += raw
		stageOfflineNodeBatch(t, repo, batch, 1.5, 2)
		assertInt64(t, db, "SELECT used_traffic FROM users WHERE id = 7", int64(rawTotal*3))
		// DB committed, ACK disappeared, and the node process restarted.
		node = nodeagent.New(nodeagent.Config{DataDir: dir})
		retry, err := node.CollectUserUsage(ctx, &nodev1.CollectUsageRequest{})
		if err != nil || !proto.Equal(batch, retry) {
			t.Fatalf("native replay changed: original=%v retry=%v err=%v", batch, retry, err)
		}
		repo = NewRepository(db, "sqlite")
		stageOfflineNodeBatch(t, repo, retry, 1.5, 2)
		assertInt64(t, db, "SELECT used_traffic FROM users WHERE id = 7", int64(rawTotal*3))
		for attempt := 0; attempt < 2; attempt++ {
			ack, err := node.AckUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: retry.GetBatchId()})
			if err != nil || !ack.GetAcknowledged() {
				t.Fatalf("native ACK: %v %v", ack, err)
			}
		}
		if iteration == 9 {
			t.Fatal("native batches did not drain")
		}
	}
	if !protocols["wireguard"] || !protocols["openvpn"] {
		t.Fatalf("missing native protocol evidence: %v", protocols)
	}
	if rawTotal < 100<<20 {
		t.Fatalf("expected two real 50 MiB quotas, got raw=%d", rawTotal)
	}
	assertInt64(t, db, "SELECT users_usage FROM admins WHERE id = 1", int64(rawTotal*3))
	assertInt64(t, db, "SELECT used_traffic FROM admins_services WHERE admin_id = 1 AND service_id = 2", int64(rawTotal*3))
	for _, protocol := range []string{"wireguard", "openvpn"} {
		var state struct {
			Pending json.RawMessage `json:"pending"`
			ACK     string          `json:"last_acked_batch_id"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, protocol, "usage-state.json"))
		if err != nil || json.Unmarshal(raw, &state) != nil || (len(state.Pending) > 0 && string(state.Pending) != "null") || state.ACK == "" {
			t.Fatalf("native %s ACK not durable: %s %v", protocol, raw, err)
		}
	}
	t.Logf("native combined lifecycle: raw=%d effective=%d unique_batches=%d; lost ACK replay exact once, coefficients=1.5*2, durable child ACKs", rawTotal, rawTotal*3, len(seen))
}
