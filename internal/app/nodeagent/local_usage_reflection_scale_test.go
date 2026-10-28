package nodeagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

// This isolates receipt decoding at the observed configured-user scale. It is
// not a native workload or evidence of production CPU acceptance.
func reflectionScaleFixture(tb testing.TB, users, inbounds int) (*Server, []localUsageReflectionQuery) {
	tb.Helper()
	s := &Server{localUsageLoaded: true, localUsageV2: &localUsageLedgerV2{Epoch: "scale", Latest: map[string]localUsageReceiptV2{}}}
	queries := make([]localUsageReflectionQuery, 0, users*inbounds)
	for user := 1; user <= users; user++ {
		totals := map[string]uint64{}
		for inbound := 0; inbound < inbounds; inbound++ {
			tag := fmt.Sprintf("inbound-%d", inbound)
			totals[usagereflection.Key("xray", tag)] = uint64(user + inbound)
			s.localUsageV2.Owners = append(s.localUsageV2.Owners, localUsageOwnerV2{Protocol: "xray", UserID: int64(user), InboundTag: tag, Total: uint64(user + inbound + 100)})
		}
		raw, err := json.Marshal(usagereflection.Watermark{Version: 2, Epoch: "scale", Root: "combined-scale", Totals: totals})
		if err != nil {
			tb.Fatal(err)
		}
		for inbound := 0; inbound < inbounds; inbound++ {
			queries = append(queries, localUsageReflectionQuery{Protocol: "xray", UserID: int64(user), InboundTag: fmt.Sprintf("inbound-%d", inbound), ReflectedLedger: string(raw)})
		}
	}
	// Match Controller's inbound-major runtime policy construction.
	sort.SliceStable(queries, func(i, j int) bool { return queries[i].InboundTag < queries[j].InboundTag })
	return s, queries
}

func TestReflectionScalePreservesEveryOwner(t *testing.T) {
	s, queries := reflectionScaleFixture(t, 650, 8)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if result.Awaiting != 100 || result.PendingReflected {
			t.Fatalf("owner %d: %+v", i, result)
		}
	}
	// A changed receipt must be decoded again on the next call.
	queries[0].ReflectedLedger = "invalid"
	if err := s.localUsageReflectionV2Locked(queries, results); err == nil {
		t.Fatal("invalid changed receipt accepted")
	}
}

func TestReflectionReceiptReuseMatchesIndependentEvaluation(t *testing.T) {
	s, queries := reflectionScaleFixture(t, 12, 3)
	// Interleave different receipts, repeat an earlier receipt, and include a
	// different epoch. Reuse may never confer authority across these boundaries.
	queries = append(queries, queries[0], queries[7], queries[0])
	var wrongEpoch usagereflection.Watermark
	if err := json.Unmarshal([]byte(queries[2].ReflectedLedger), &wrongEpoch); err != nil {
		t.Fatal(err)
	}
	wrongEpoch.Epoch = "other-epoch"
	raw, err := json.Marshal(wrongEpoch)
	if err != nil {
		t.Fatal(err)
	}
	queries[2].ReflectedLedger = string(raw)
	queries[4].ReflectedLedger = ""
	queries[5].PendingChildID = "unrelated-child"
	batch := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, batch); err != nil {
		t.Fatal(err)
	}
	for i, query := range queries {
		individual := make([]localUsageReflectionResult, 1)
		if err := s.localUsageReflectionV2Locked([]localUsageReflectionQuery{query}, individual); err != nil {
			t.Fatal(err)
		}
		if batch[i] != individual[0] {
			t.Fatalf("query %d: batch=%+v individual=%+v", i, batch[i], individual[0])
		}
	}
}

func TestReflectionReceiptCacheBoundsDoNotDropOwners(t *testing.T) {
	// More distinct receipts than the entry bound must still evaluate every
	// owner. Oversized valid receipts bypass the byte bound without rejection.
	s, queries := reflectionScaleFixture(t, 1300, 2)
	var large usagereflection.Watermark
	if err := json.Unmarshal([]byte(queries[0].ReflectedLedger), &large); err != nil {
		t.Fatal(err)
	}
	large.Root = strings.Repeat("r", (1<<20)+1)
	raw, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	queries[0].ReflectedLedger = string(raw)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if result.Awaiting != 100 {
			t.Fatalf("owner %d: %+v", i, result)
		}
	}
}

func BenchmarkReflection650UsersEightInbounds(b *testing.B) {
	s, queries := reflectionScaleFixture(b, 650, 8)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
			b.Fatal(err)
		}
	}
}

// The reflection hot path resolves one cumulative receipt key per query on
// every tick. The key is cached per owner in the index, so a warm steady-state
// tick must not allocate for it. This bounds the production-repeatable cost
// without touching accounting semantics.
func TestReflectionSteadyStateTickDoesNotAllocate(t *testing.T) {
	s, queries := reflectionScaleFixture(t, 650, 8)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(20, func() {
		if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
			t.Fatal(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("warm reflection tick allocated %v times; want 0", allocations)
	}
}

// Every cached owner key must match the uncached encoding, so the cache can
// never attribute usage to a different owner.
func TestReflectionUsageKeyCacheMatchesUncachedEncoding(t *testing.T) {
	s, _ := reflectionScaleFixture(t, 24, 3)
	idx := s.localUsageV2IndexLocked()
	if len(idx.UsageKeys) != len(idx.Owners) {
		t.Fatalf("cache holds %d keys for %d owners", len(idx.UsageKeys), len(idx.Owners))
	}
	for key := range idx.Owners {
		if got, want := idx.usageKey(key), usageKeyForOwner(key); got != want {
			t.Fatalf("owner %+v cached key %q want %q", key, got, want)
		}
	}
	// A miss must still encode the right key rather than return "".
	missing := localUsageOwnerKey{Protocol: "xray", UserID: 9999, InboundTag: "inbound-9"}
	if _, ok := idx.Owners[missing]; ok {
		t.Fatal("fixture unexpectedly contains the probe owner")
	}
	if got, want := idx.usageKey(missing), usageKeyForOwner(missing); got != want {
		t.Fatalf("cache miss key %q want %q", got, want)
	}
}
