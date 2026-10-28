package nodeagent

import (
	"context"
	"fmt"
	"github.com/antimage/antimage/internal/platform/usagereflection"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectUserUsageContinuesPastLegacyDeliveryCapacity(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayUsageLoaded = true
	for i := 0; i < 5000; i++ {
		id := fmt.Sprintf("xray-long-run-%d", i)
		s.xrayUsageMu.Lock()
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: id, Samples: []xrayUsageSample{{UserID: 11, InboundTag: "long", Value: 1}}}
		s.xrayUsageMu.Unlock()
		// Exercise the legacy wire request. It must remain usable even though it
		// does not ask for reflection receipts; persistence still uses the bounded
		// v2 ledger internally.
		batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
		if err != nil {
			t.Fatalf("collection %d: %v", i, err)
		}
		if batch.GetBatchId() != id || len(batch.GetReflectionReceipts()) != 0 {
			t.Fatalf("legacy collection %d changed its response contract: %+v", i, batch)
		}
		ack, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: id})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK %d: %v %v", i, ack, err)
		}
	}
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	if restarted.localUsageV2 == nil || len(restarted.localUsageV2.Owners) != 1 || restarted.localUsageV2.Owners[0].Total != 5000 || len(restarted.localUsageV2.Latest) != 1 || len(restarted.localUsageDeliveries) != 0 {
		t.Fatalf("ledger grew with batch count: %+v", restarted.localUsageV2)
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 32<<10 {
		t.Fatalf("steady-state ledger is not bounded: %d bytes", len(raw))
	}
}

func TestCollectUserUsageMigratesFullProductionLedger(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	fixture := largeUsageDeliveryFixture()
	// Prove the exact production-shaped v1 ledger is accepted by the legacy RPC
	// request after migration to bounded cumulative state.
	legacyProbe := New(Config{DataDir: t.TempDir()})
	if err := legacyProbe.persistLocalUsageDeliveriesLocked(fixture); err != nil {
		t.Fatal(err)
	}
	legacyProbe.xrayUsageLoaded = true
	legacyProbe.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-capacity-regression", Samples: []xrayUsageSample{{UserID: 7, InboundTag: "inbound-0", Value: 123}}}
	if err := legacyProbe.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	// Reproduce the exact production failure in the capacity-limited v1 batch
	// primitive. The RPC path below must stop routing new deliveries through
	// that primitive once the ledger is migrated to bounded cumulative state.
	if err := legacyProbe.recordLocalUsageDelivery("xray-capacity-regression"); err == nil || err.Error() != "unreflected accounting delivery capacity exceeded" {
		t.Fatalf("production capacity failure was not reproduced exactly: %v", err)
	}
	legacyBatch, err := legacyProbe.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatalf("legacy CollectUserUsage rejected full production ledger: %v", err)
	}
	if legacyBatch.GetBatchId() != "xray-capacity-regression" || len(legacyBatch.GetReflectionReceipts()) != 0 {
		t.Fatalf("legacy response contract changed: %+v", legacyBatch)
	}
	if legacyProbe.localUsageV2 == nil || len(legacyProbe.localUsageDeliveries) != 4096 {
		t.Fatal("legacy collector did not migrate while preserving unproven anchors")
	}
	legacyAck, err := legacyProbe.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: legacyBatch.GetBatchId()})
	if err != nil || !legacyAck.GetAcknowledged() || len(legacyProbe.localUsageDeliveries) != 4096 {
		t.Fatalf("legacy ACK retired anchors without reflection proof: ack=%+v err=%v anchors=%d", legacyAck, err, len(legacyProbe.localUsageDeliveries))
	}

	var inbound string
	var legacyRaw uint64
	legacyBatches := map[string]uint64{}
	for _, delivery := range fixture {
		for _, row := range delivery.Rows {
			if row.Protocol == "xray" && row.UserID == 7 {
				if inbound == "" {
					inbound = row.InboundTag
				}
				if row.InboundTag == inbound {
					legacyRaw += row.Raw
					legacyBatches[delivery.BatchID] += row.Raw
				}
			}
		}
	}
	if inbound == "" || legacyRaw == 0 {
		t.Fatal("fixture lacks the selected Xray owner")
	}
	if err := s.persistLocalUsageDeliveriesLocked(fixture); err != nil {
		t.Fatal(err)
	}
	s.xrayUsageLoaded = true
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-capacity-regression", Samples: []xrayUsageSample{{UserID: 7, InboundTag: inbound, Value: 123}}}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{ReflectionVersion: 2})
	if err != nil {
		t.Fatalf("CollectUserUsage must survive a full v1 ledger: %v", err)
	}
	if batch.GetBatchId() != "xray-capacity-regression" {
		t.Fatalf("unexpected batch: %q", batch.GetBatchId())
	}
	receipt, ok := batch.GetReflectionReceipts()[7]
	if !ok {
		t.Fatal("v2 response omitted owner watermark")
	}
	w, err := usagereflection.Decode(receipt)
	if err != nil || w.Root != batch.GetBatchId() {
		t.Fatalf("invalid reflection receipt %+v %v", w, err)
	}
	key := usagereflection.Key("xray", inbound)
	if w.Totals[key] != legacyRaw+123 || w.PendingChildren[key] != (usagereflection.PendingChild{BatchID: "xray-capacity-regression", Raw: 123}) {
		t.Fatalf("receipt lost production legacy/current bytes: total=%d old=%d pending=%+v", w.Totals[key], legacyRaw, w.PendingChildren[key])
	}
	var receiptLegacy uint64
	for root, raw := range w.LegacyBatches[key] {
		receiptLegacy += raw
		if legacyBatches[root] != raw {
			t.Fatalf("legacy root %s raw=%d want=%d", root, raw, legacyBatches[root])
		}
	}
	if receiptLegacy != legacyRaw || len(w.LegacyBatches[key]) != len(legacyBatches) {
		t.Fatalf("legacy receipt does not conserve bytes: got=%d want=%d roots=%d wantRoots=%d", receiptLegacy, legacyRaw, len(w.LegacyBatches[key]), len(legacyBatches))
	}
	// Restart reloads the compact v2 owner ledger and content-addressed v1 anchors.
	restarted := New(Config{DataDir: s.cfg.DataDir})
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	if restarted.localUsageV2 == nil || len(restarted.localUsageV2.Owners) == 0 || len(restarted.localUsageDeliveries) != 4096 {
		t.Fatal("v1 migration anchors or v2 owner state were not recovered")
	}
	if _, err := restarted.localAwaitingReflectionUsage("xray", 7, inbound, "xray-capacity-regression", ""); err != nil {
		t.Fatal(err)
	}
	if len(restarted.localUsageDeliveries) != 4096 {
		t.Fatal("legacy anchors were removed without a committed reflection watermark")
	}
	ack, err := restarted.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: "xray-capacity-regression"})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("ACK without panel reflection proof: %+v err=%v", ack, err)
	}
	if len(restarted.localUsageDeliveries) != 4096 {
		t.Fatal("raw transport ACK was incorrectly treated as authority to retire legacy anchors")
	}
}

func TestV2DeliveryPersistenceFailureRollsBackAndReplaysAfterRestart(t *testing.T) {
	dataDir := t.TempDir()
	s := New(Config{DataDir: dataDir})
	s.xrayUsageLoaded = true
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-persist-retry", Samples: []xrayUsageSample{{UserID: 17, InboundTag: "in", Value: 321}}}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDir, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.DataDir = badDir
	if _, err := s.recordLocalUsageDeliveryV2("xray-persist-retry"); err == nil {
		t.Fatal("expected durable write failure")
	}
	if s.localUsageV2 != nil || len(s.localUsageDeliveries) != 0 {
		t.Fatal("failed persistence mutated in-memory accounting state")
	}
	s.cfg.DataDir = dataDir
	first, err := s.recordLocalUsageDeliveryV2("xray-persist-retry")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.recordLocalUsageDeliveryV2("xray-persist-retry")
	if err != nil {
		t.Fatal(err)
	}
	if first[17] == "" || first[17] != replayed[17] || s.localUsageV2.Owners[0].Total != 321 {
		t.Fatalf("retry changed receipt or double-counted: first=%s replay=%s ledger=%+v", first[17], replayed[17], s.localUsageV2)
	}
	restarted := New(Config{DataDir: dataDir})
	batch, err := restarted.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{ReflectionVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if batch.GetBatchId() != "xray-persist-retry" || batch.GetReflectionReceipts()[17] != first[17] {
		t.Fatalf("restart replay changed delivery: batch=%+v", batch)
	}
	ack, err := restarted.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()})
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	final := New(Config{DataDir: dataDir})
	if err := final.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	if len(final.localUsageV2.Owners) != 1 || final.localUsageV2.Owners[0].Total != 321 {
		t.Fatalf("ACK changed cumulative total: %+v", final.localUsageV2)
	}
}

func TestLegacyControllerRootCursorResolvesAgainstDurableReceipt(t *testing.T) {
	dataDir := t.TempDir()
	s := New(Config{DataDir: dataDir})
	s.localUsageLoaded = true
	// Seed a v1 migration anchor so the owner carries LegacyTotal, just like a
	// node that upgraded from the capacity-limited ledger.
	seed := []localUsageDelivery{{
		BatchID: "xray-legacy-seed",
		Rows:    []localUsageDeliveryRow{{Protocol: "xray", ChildID: "xray-seed-child", UserID: 23, InboundTag: "legacy-in", Raw: 100}},
	}}
	if err := s.persistLocalUsageDeliveriesLocked(seed); err != nil {
		t.Fatal(err)
	}
	s.localUsageDeliveries = seed
	s.xrayUsageLoaded = true
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-legacy-cursor", Samples: []xrayUsageSample{{UserID: 23, InboundTag: "legacy-in", Value: 444}}}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	// Legacy controller: no reflection receipts in the response, but it still
	// acknowledges by the root batch id.
	batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.GetReflectionReceipts()) != 0 {
		t.Fatal("legacy controller unexpectedly received reflection receipts")
	}
	awaiting, err := s.localAwaitingReflectionUsage("xray", 23, "legacy-in", "", batch.GetBatchId())
	if err != nil {
		t.Fatal(err)
	}
	if awaiting != 0 {
		t.Fatalf("legacy root cursor left %d bytes unreflected", awaiting)
	}
	// Resolving our own local receipt must not be mistaken for panel authority
	// to retire the migration anchor; that still requires a committed proof.
	if s.localUsageV2.Owners[0].LegacyTotal != 100 || len(s.localUsageDeliveries) != 1 {
		t.Fatalf("local receipt was treated as a durable retirement proof: ledger=%+v anchors=%+v", s.localUsageV2.Owners, s.localUsageDeliveries)
	}
	restarted := New(Config{DataDir: dataDir})
	awaiting, err = restarted.localAwaitingReflectionUsage("xray", 23, "legacy-in", "", "xray-legacy-cursor")
	if err != nil || awaiting != 0 {
		t.Fatalf("restart awaiting=%d err=%v", awaiting, err)
	}
	if len(restarted.localUsageDeliveries) != 1 || restarted.localUsageV2.Owners[0].LegacyTotal != 100 {
		t.Fatal("restart retired an unproven legacy anchor")
	}
}
