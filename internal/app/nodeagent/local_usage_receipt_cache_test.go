package nodeagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

func receiptCacheFixture(tb testing.TB, owners int) (*Server, []localUsageReflectionQuery, []string) {
	tb.Helper()
	s := &Server{localUsageLoaded: true, localUsageV2: &localUsageLedgerV2{Epoch: "cache", Latest: map[string]localUsageReceiptV2{}}}
	queries := make([]localUsageReflectionQuery, 0, owners)
	raws := make([]string, 0, owners)
	for user := 1; user <= owners; user++ {
		tag := "inbound"
		totals := map[string]uint64{usagereflection.Key("xray", tag): uint64(user)}
		raw, err := json.Marshal(usagereflection.Watermark{Version: 2, Epoch: "cache", Root: "combined-cache", Totals: totals})
		if err != nil {
			tb.Fatal(err)
		}
		s.localUsageV2.Owners = append(s.localUsageV2.Owners, localUsageOwnerV2{
			Protocol: "xray", UserID: int64(user), InboundTag: tag, Total: uint64(user + 10),
		})
		queries = append(queries, localUsageReflectionQuery{Protocol: "xray", UserID: int64(user), InboundTag: tag, ReflectedLedger: string(raw)})
		raws = append(raws, string(raw))
	}
	return s, queries, raws
}

// The cache must never change the answer, only the cost. Evaluating with and
// without a warm cache has to agree for every owner.
func TestReceiptCacheMatchesUncachedEvaluation(t *testing.T) {
	warm, queries, _ := receiptCacheFixture(t, 40)
	warmResults := make([]localUsageReflectionResult, len(queries))
	if err := warm.localUsageReflectionV2Locked(queries, warmResults); err != nil {
		t.Fatal(err)
	}
	// Second pass is served entirely from the cache.
	warmAgain := make([]localUsageReflectionResult, len(queries))
	if err := warm.localUsageReflectionV2Locked(queries, warmAgain); err != nil {
		t.Fatal(err)
	}
	cold, coldQueries, _ := receiptCacheFixture(t, 40)
	coldResults := make([]localUsageReflectionResult, len(coldQueries))
	if cold.localUsageReceiptCache != nil && len(cold.localUsageReceiptCache.entries) != 0 {
		t.Fatalf("a fresh server must start with an empty receipt cache")
	}
	if err := cold.localUsageReflectionV2Locked(coldQueries, coldResults); err != nil {
		t.Fatal(err)
	}
	if cold.localUsageReceiptCache == nil || len(cold.localUsageReceiptCache.entries) == 0 {
		t.Fatalf("expected the cold server to populate the cache after decoding")
	}
	for i := range warmAgain {
		if warmAgain[i] != warmResults[i] || warmResults[i] != coldResults[i] {
			t.Fatalf("owner %d: warm=%+v refresh=%+v cold=%+v", i, warmResults[i], warmAgain[i], coldResults[i])
		}
	}
}

// A replaced ledger must never be evaluated against a stale binding. Entries
// decoded for a superseded ledger may linger, but they are keyed by the exact
// receipt string, so a later decode rebinds the cache to the current ledger and
// any served entry is still the decode of the bytes the caller passed.
func TestReceiptCacheRebindsAfterLedgerReplacement(t *testing.T) {
	s, queries, _ := receiptCacheFixture(t, 4)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	if len(s.localUsageReceiptCache.entries) == 0 {
		t.Fatal("expected the receipt cache to retain decoded entries")
	}
	previousLedger := s.localUsageV2
	// Replace the ledger with one that still owns the same keys so the next
	// evaluation reaches the decode path and rebinds the cache.
	replacement := &localUsageLedgerV2{Epoch: "cache", Latest: map[string]localUsageReceiptV2{}}
	replacement.Owners = append(replacement.Owners, previousLedger.Owners...)
	for i := range replacement.Owners {
		replacement.Owners[i].Total += 1000
	}
	s.localUsageV2 = replacement
	s.localUsageV2Index = nil
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	if s.localUsageV2 == previousLedger {
		t.Fatal("test did not replace the ledger")
	}
	if s.localUsageReceiptCache == nil || s.localUsageReceiptCache.ledger != s.localUsageV2 {
		t.Fatal("cache did not rebind to the replacement ledger")
	}
	for i, result := range results {
		if result.Awaiting != 1010 {
			t.Fatalf("owner %d used a stale ledger: %+v", i, result)
		}
	}
}

// A changed receipt string is a new key, so a stale count can never be served.
func TestReceiptCacheRepresentsChangedReceipts(t *testing.T) {
	s, queries, _ := receiptCacheFixture(t, 2)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	advanced := usagereflection.Watermark{Version: 2, Epoch: "cache", Root: "combined-cache",
		Totals: map[string]uint64{usagereflection.Key("xray", "inbound"): 11}}
	raw, err := json.Marshal(advanced)
	if err != nil {
		t.Fatal(err)
	}
	queries[0].ReflectedLedger = string(raw)
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	// user 1 owns Total 11 and is now fully reflected; user 2 is untouched.
	if results[0].Awaiting != 0 || results[1].Awaiting != 10 {
		t.Fatalf("changed receipt was not reflected: %+v vs %+v", results[0], results[1])
	}
}

// An oversized receipt is always decoded fresh and never rejected.
func TestReceiptCacheBypassesOversizedReceipts(t *testing.T) {
	s, queries, raws := receiptCacheFixture(t, 1)
	results := make([]localUsageReflectionResult, 1)
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	if _, present := s.localUsageReceiptCache.entries[raws[0]]; !present {
		t.Fatal("small receipt was not retained")
	}
	large := usagereflection.Watermark{Version: 2, Epoch: "cache", Root: "combined-cache",
		Totals: map[string]uint64{usagereflection.Key("xray", "inbound"): 7}}
	large.Root = strings.Repeat("r", localUsageReceiptCacheMaxReceipt+1)
	raw, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	queries[0].ReflectedLedger = string(raw)
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	if results[0].Awaiting != 4 {
		t.Fatalf("unexpected awaiting: %+v", results[0])
	}
	if _, retained := s.localUsageReceiptCache.entries[string(raw)]; retained {
		t.Fatal("oversized receipt was retained")
	}
}

// The entry bound must not turn into a correctness failure at production scale.
func TestReceiptCacheEntryBoundStillEvaluatesEveryOwner(t *testing.T) {
	owners := localUsageReceiptCacheMaxEntries + 64
	s, queries, _ := receiptCacheFixture(t, owners)
	results := make([]localUsageReflectionResult, len(queries))
	if err := s.localUsageReflectionV2Locked(queries, results); err != nil {
		t.Fatal(err)
	}
	for i, result := range results {
		if result.Awaiting != 10 {
			t.Fatalf("owner %d: %+v", i, result)
		}
	}
	if len(s.localUsageReceiptCache.entries) > localUsageReceiptCacheMaxEntries {
		t.Fatalf("cache exceeded its entry bound: %d", len(s.localUsageReceiptCache.entries))
	}
}
