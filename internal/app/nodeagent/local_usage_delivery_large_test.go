package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

// Match the production failure shape: 4096 roots, 4056 combined, 6194 rows.
func largeUsageDeliveryFixture() []localUsageDelivery {
	deliveries := make([]localUsageDelivery, 4096)
	const total uint64 = 12454882550
	const rows = 6194
	rowIndex := 0
	for i := range deliveries {
		prefix := "combined"
		if i >= 4056 {
			prefix = "xray"
		}
		deliveries[i].BatchID = fmt.Sprintf("%s-%d", prefix, i+1)
		count := 1
		if i < rows-len(deliveries) {
			count++
		}
		for j := 0; j < count; j++ {
			protocol := "xray"
			if rowIndex%3 == 1 {
				protocol = "wireguard"
			}
			if rowIndex%3 == 2 {
				protocol = "l2tp"
			}
			raw := total / rows
			if rowIndex == rows-1 {
				raw += total % rows
			}
			deliveries[i].Rows = append(deliveries[i].Rows, localUsageDeliveryRow{Protocol: protocol, ChildID: fmt.Sprintf("%s-%d", protocol, i+1), UserID: int64(rowIndex%512 + 1), InboundTag: fmt.Sprintf("inbound-%d", rowIndex%4), Raw: raw})
			rowIndex++
		}
	}
	return deliveries
}

func TestV2ReflectionCompactsOnlyReflectedLegacyOwner(t *testing.T) {
	dir := t.TempDir()
	deliveries := largeUsageDeliveryFixture()
	s := &Server{cfg: Config{DataDir: dir}, localUsageLoaded: true, localUsageDeliveries: deliveries}
	ledger, err := s.migratedLocalUsageV2Locked()
	if err != nil {
		t.Fatal(err)
	}
	s.localUsageV2 = ledger
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Owners) == 0 {
		t.Fatal("fixture did not migrate any owners")
	}
	owner := ledger.Owners[0]
	key := usagereflection.Key(owner.Protocol, owner.InboundTag)
	encoded, err := json.Marshal(usagereflection.Watermark{Version: 2, Epoch: ledger.Epoch, Root: "combined-reflected", Totals: map[string]uint64{key: owner.Total}, Baselines: map[string]uint64{key: owner.LegacyTotal}})
	if err != nil {
		t.Fatal(err)
	}
	queries := []localUsageReflectionQuery{{Protocol: owner.Protocol, UserID: owner.UserID, InboundTag: owner.InboundTag, ReflectedLedger: string(encoded)}}
	results := make([]localUsageReflectionResult, 1)
	if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		t.Fatal(err)
	}
	if results[0].Awaiting != 0 {
		t.Fatalf("reflected owner still awaiting %d bytes", results[0].Awaiting)
	}
	var before, after uint64
	for _, d := range deliveries {
		for _, row := range d.Rows {
			before += row.Raw
		}
	}
	for _, d := range s.localUsageDeliveries {
		for _, row := range d.Rows {
			if row.UserID == owner.UserID && row.Protocol == owner.Protocol && row.InboundTag == owner.InboundTag {
				t.Fatal("reflected legacy owner remained pinned in anchors")
			}
			after += row.Raw
		}
	}
	if before != after+owner.Total {
		t.Fatalf("anchor conservation before=%d after=%d compacted=%d", before, after, owner.Total)
	}
	if s.localUsageV2.Owners[s.localUsageV2IndexLocked().Owners[localUsageOwnerKey{owner.Protocol, owner.UserID, owner.InboundTag}]].LegacyTotal != 0 {
		t.Fatal("compacted owner still has a legacy baseline")
	}

	restarted := &Server{cfg: Config{DataDir: dir}}
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	results = make([]localUsageReflectionResult, 1)
	if err := restarted.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		t.Fatal(err)
	}
	if results[0].Awaiting != 0 {
		t.Fatalf("restart lost reflected watermark: awaiting=%d", results[0].Awaiting)
	}
	if len(restarted.localUsageDeliveries) >= len(deliveries) {
		t.Fatalf("legacy anchors were not compacted: got %d roots from %d", len(restarted.localUsageDeliveries), len(deliveries))
	}
}

func TestLocalDeliveryProductionShapeConservesBytes(t *testing.T) {
	deliveries := largeUsageDeliveryFixture()
	var sum uint64
	rows := 0
	for _, d := range deliveries {
		for _, r := range d.Rows {
			rows++
			sum += r.Raw
		}
	}
	if len(deliveries) != 4096 || rows != 6194 || sum != 12454882550 {
		t.Fatalf("fixture roots=%d rows=%d raw=%d", len(deliveries), rows, sum)
	}
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	var observed uint64
	for uid := int64(1); uid <= 512; uid++ {
		for _, protocol := range []string{"xray", "wireguard", "l2tp"} {
			for inbound := 0; inbound < 4; inbound++ {
				raw, err := s.localAwaitingReflectionUsage(protocol, uid, fmt.Sprintf("inbound-%d", inbound), "", "")
				if err != nil {
					t.Fatal(err)
				}
				observed += raw
			}
		}
	}
	if observed != sum {
		t.Fatalf("accounting conservation: got %d want %d", observed, sum)
	}
}

func BenchmarkLocalDeliveryProductionStaleReflection(b *testing.B) {
	s := &Server{cfg: Config{DataDir: b.TempDir()}}
	if err := s.persistLocalUsageDeliveriesLocked(largeUsageDeliveryFixture()); err != nil {
		b.Fatal(err)
	}
	queries := make([]localUsageReflectionQuery, 512)
	for i := range queries {
		queries[i] = localUsageReflectionQuery{Protocol: "xray", UserID: int64(i + 1), InboundTag: fmt.Sprintf("inbound-%d", i%4), ReflectedRootBatchID: "stale-unknown"}
	}
	var results []localUsageReflectionResult
	if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLocalDeliveryIndexInvalidationAndFailedPersistence(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}, xrayUsageLoaded: true}
	record := func(id string, raw uint64) {
		t.Helper()
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: id, Samples: []xrayUsageSample{{UserID: 7, InboundTag: "a", Value: raw}}}
		if err := s.recordLocalUsageDelivery(id); err != nil {
			t.Fatal(err)
		}
	}
	query := func(want uint64, root string) {
		t.Helper()
		got, err := s.localAwaitingReflectionUsage("xray", 7, "a", "", root)
		if err != nil || got != want {
			t.Fatalf("got=%d want=%d err=%v", got, want, err)
		}
	}
	record("xray-first", 100)
	query(100, "")
	firstIndex := s.localUsageDeliveryIndex
	if firstIndex == nil {
		t.Fatal("query did not build the derived index")
	}
	record("xray-second", 50)
	if s.localUsageDeliveryIndex != nil {
		t.Fatal("successful write retained stale index")
	}
	query(150, "")
	query(0, "xray-second")
	query(0, "")
	record("xray-third", 25)
	query(25, "")
	priorIndex := s.localUsageDeliveryIndex
	originalDir := s.cfg.DataDir
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.DataDir = blocked
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-failed", Samples: []xrayUsageSample{{UserID: 7, InboundTag: "a", Value: 999}}}
	if err := s.recordLocalUsageDelivery("xray-failed"); err == nil {
		t.Fatal("expected real persistence failure")
	}
	s.cfg.DataDir = originalDir
	if s.localUsageDeliveryIndex != priorIndex {
		t.Fatal("failed persistence invalidated committed index")
	}
	query(25, "")
	restarted := &Server{cfg: s.cfg}
	got, err := restarted.localAwaitingReflectionUsage("xray", 7, "a", "", "")
	if err != nil || got != 25 {
		t.Fatalf("restart got=%d err=%v", got, err)
	}
}

func TestLocalDeliveryIndexedReadsConcurrentWithDelivery(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}, xrayUsageLoaded: true}
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 40; i++ {
				if _, err := s.localAwaitingReflectionUsage("xray", 7, "a", "", ""); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("xray-concurrent-%d", i)
		s.xrayUsageMu.Lock()
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: id, Samples: []xrayUsageSample{{UserID: 7, InboundTag: "a", Value: 1}}}
		s.xrayUsageMu.Unlock()
		if err := s.recordLocalUsageDelivery(id); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
	got, err := s.localAwaitingReflectionUsage("xray", 7, "a", "", "")
	if err != nil || got != 20 {
		t.Fatalf("concurrent ledger total=%d err=%v", got, err)
	}
}

func TestLocalDeliveryIndexedSteadyStateAllocationBudget(t *testing.T) {
	s := &Server{localUsageLoaded: true, localUsageDeliveries: largeUsageDeliveryFixture()}
	queries := make([]localUsageReflectionQuery, 512)
	for i := range queries {
		queries[i] = localUsageReflectionQuery{Protocol: "xray", UserID: int64(i + 1), InboundTag: fmt.Sprintf("inbound-%d", i%4), ReflectedRootBatchID: "stale-unknown"}
	}
	var results []localUsageReflectionResult
	if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(50, func() {
		if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
			t.Fatal(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("steady-state reflection allocated %f times; want zero", allocations)
	}
}

func TestLocalDeliveryProductionShapeMixedReflectionConservation(t *testing.T) {
	deliveries := largeUsageDeliveryFixture()
	type marker struct {
		root     string
		position int
	}
	reflected := map[int64]marker{}
	for position, delivery := range deliveries {
		for _, row := range delivery.Rows {
			if row.UserID%4 == 0 {
				reflected[row.UserID] = marker{delivery.BatchID, position}
			}
		}
	}
	expected := map[localUsageReflectionQuery]uint64{}
	queries := make([]localUsageReflectionQuery, 0, 512*12)
	var reflectedRaw uint64
	for position, delivery := range deliveries {
		for _, row := range delivery.Rows {
			m, exists := reflected[row.UserID]
			if exists && position <= m.position {
				reflectedRaw += row.Raw
				continue
			}
			key := localUsageReflectionQuery{Protocol: row.Protocol, UserID: row.UserID, InboundTag: row.InboundTag}
			expected[key] += row.Raw
		}
	}
	for uid := int64(1); uid <= 512; uid++ {
		for _, protocol := range []string{"xray", "wireguard", "l2tp"} {
			for tag := 0; tag < 4; tag++ {
				root := "stale-unknown"
				if m, ok := reflected[uid]; ok {
					root = m.root
				}
				queries = append(queries, localUsageReflectionQuery{Protocol: protocol, UserID: uid, InboundTag: fmt.Sprintf("inbound-%d", tag), ReflectedRootBatchID: root})
			}
		}
	}
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	results, err := s.localAwaitingReflectionUsageBatch(queries)
	if err != nil {
		t.Fatal(err)
	}
	var awaiting uint64
	for i, query := range queries {
		key := query
		key.ReflectedRootBatchID = ""
		if results[i].Awaiting != expected[key] {
			t.Fatalf("owner %v got=%d want=%d", key, results[i].Awaiting, expected[key])
		}
		awaiting += results[i].Awaiting
	}
	if awaiting+reflectedRaw != 12454882550 {
		t.Fatalf("lost accounting bytes: awaiting=%d reflected=%d", awaiting, reflectedRaw)
	}
	restarted := &Server{cfg: s.cfg}
	again, err := restarted.localAwaitingReflectionUsageBatch(queries)
	if err != nil {
		t.Fatal(err)
	}
	for i := range results {
		if again[i] != results[i] {
			t.Fatalf("restart changed query %d: %+v != %+v", i, again[i], results[i])
		}
	}
}
