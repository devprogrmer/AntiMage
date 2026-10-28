package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// referenceAnchorSidecar recomputes the migration-anchor document and hash the
// way the pre-optimization writer did, so the memoized path can be compared
// byte-for-byte against the reference semantics.
func referenceAnchorSidecar(t *testing.T, deliveries []localUsageDelivery) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(localUsageDeliveryState{Version: 1, Deliveries: deliveries})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

// TestLocalUsageAnchorSidecarReusesUnchangedSlice proves the memoization only
// ever returns the exact bytes and hash the reference computation would have
// produced for the same anchor slice, and that a changed anchor set (any new
// slice) always misses the cache.
func TestLocalUsageAnchorSidecarReusesUnchangedSlice(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	fixture := steadyStateLegacyFixture(64, 8, 4)

	raw, hash, err := s.anchorSidecarLocked(fixture)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, wantHash := referenceAnchorSidecar(t, fixture)
	if string(raw) != string(wantRaw) || hash != wantHash {
		t.Fatalf("anchor sidecar mismatch: hash=%s want=%s", hash, wantHash)
	}

	// The identical slice must reuse the cached bytes and hash.
	raw2, hash2, err := s.anchorSidecarLocked(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if &raw2[0] != &raw[0] || hash2 != hash {
		t.Fatal("unchanged anchor slice did not reuse the memoized sidecar")
	}

	// A changed anchor set is a different slice and must be re-serialized.
	changed := append([]localUsageDelivery(nil), fixture...)
	changed[0].Rows = append([]localUsageDeliveryRow(nil), changed[0].Rows...)
	changed[0].Rows[0].Raw++
	raw3, hash3, err := s.anchorSidecarLocked(changed)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw3, wantHash3 := referenceAnchorSidecar(t, changed)
	if string(raw3) != string(wantRaw3) || hash3 != wantHash3 {
		t.Fatalf("changed anchor sidecar mismatch: hash=%s want=%s", hash3, wantHash3)
	}
	if hash3 == hash {
		t.Fatal("changed anchor set reused the stale hash")
	}
}

// TestLocalUsageAnchorSidecarEmptySliceRoundTrips guards the zero-length edge
// case: the writer must still produce the reference document.
func TestLocalUsageAnchorSidecarEmptySliceRoundTrips(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	raw, hash, err := s.anchorSidecarLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, wantHash := referenceAnchorSidecar(t, nil)
	if string(raw) != string(wantRaw) || hash != wantHash {
		t.Fatalf("empty anchor sidecar mismatch: hash=%s want=%s", hash, wantHash)
	}
}

// TestLocalUsageAnchorCacheConcurrentMutationsStayConsistent drives the real
// collection path from multiple goroutines against one Server. Every durable
// write must agree with the reference serialization of the anchor set it was
// given, so a cache keyed on the wrong identity cannot silently publish stale
// bytes. It also exercises the on-disk reload to confirm persisted state is
// still self-consistent.
func TestLocalUsageAnchorCacheConcurrentMutationsStayConsistent(t *testing.T) {
	dataDir := t.TempDir()
	s := New(Config{DataDir: dataDir})
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"target":"node:9","session_callback":{"node_id":9}}`}); err != nil {
		t.Fatal(err)
	}
	// Seed a v1 anchor set so the v2 collection path re-persists a non-empty,
	// unchanged anchor slice on every call, which is the production shape the
	// memoization targets.
	fixture := steadyStateLegacyFixture(96, 8, 4)
	if err := s.persistLocalUsageDeliveriesLocked(fixture); err != nil {
		t.Fatal(err)
	}
	s.localUsageDeliveries = fixture
	s.localUsageDeliveryIndex = nil
	s.xrayUsageLoaded = true

	// Production runs one serial collection worker per protocol while many
	// readers (quota evaluation, reflection queries) run concurrently. Both
	// sides take localUsageMu, so the memoized cache must be safe and correct
	// under that mixed access. The writers stay serial; the readers overlap.
	const iterations = 200
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < iterations; i++ {
			batchID := fmt.Sprintf("xray-anchor-cache-%d", i)
			s.xrayUsageMu.Lock()
			s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: batchID, Samples: []xrayUsageSample{{UserID: int64(i%8 + 1), InboundTag: "inbound-0", Value: uint64(i + 1)}}}
			s.xrayUsageMu.Unlock()
			batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{ReflectionVersion: 2})
			if err != nil {
				t.Errorf("collect %s: %v", batchID, err)
				return
			}
			if _, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()}); err != nil {
				t.Errorf("ack %s: %v", batchID, err)
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			queries := steadyStateMixedQueries(s, 8, 4, fixtureRootIndex(fixture))
			var results []localUsageReflectionResult
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
					t.Errorf("reflection reader %d: %v", reader, err)
					return
				}
			}
		}(reader)
	}
	wg.Wait()

	s.localUsageMu.Lock()
	anchors := append([]localUsageDelivery(nil), s.localUsageDeliveries...)
	cache := s.localUsageAnchorCache
	var cachedRaw []byte
	var cachedHash string
	if cache != nil {
		cachedRaw = append([]byte(nil), cache.raw...)
		cachedHash = cache.hash
	}
	s.localUsageMu.Unlock()

	// The v2 path may have compacted every anchor away, in which case nothing is
	// cached; otherwise the memoized bytes must match the persisted anchor set.
	wantRaw, wantHash := referenceAnchorSidecar(t, anchors)
	if len(anchors) > 0 {
		if string(cachedRaw) != string(wantRaw) || cachedHash != wantHash {
			t.Fatalf("memoized anchor sidecar diverged from the persisted anchor set: hash=%s want=%s", cachedHash, wantHash)
		}
	}

	// A fresh instance must load the same durable state (anchors plus the
	// cumulative ledger) and agree with the in-memory anchor document.
	restarted := New(Config{DataDir: dataDir})
	if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
		t.Fatal(err)
	}
	if restarted.localUsageLegacyHash != cachedHash {
		t.Fatalf("persisted anchor hash mismatch after reload: got %s want %s", restarted.localUsageLegacyHash, cachedHash)
	}
	reloadRaw, reloadHash := referenceAnchorSidecar(t, restarted.localUsageDeliveries)
	if string(reloadRaw) != string(wantRaw) || reloadHash != wantHash {
		t.Fatal("reloaded anchor set differs from the in-memory set")
	}
}

// TestLocalUsageAnchorProducersNeverMutateCachedSliceInPlace enforces the
// invariant the anchor memoization depends on: every producer of the migration
// anchor set must publish a *new* slice (copy-on-write) and must never edit a
// row through an already-cached backing array. If a future change starts
// mutating in place, the identity-keyed cache would replay stale bytes and this
// test fails immediately.
//
// The retained slice is deep-snapshotted before the producers run and compared
// afterwards, so an in-place edit of any row field is detected.
func TestLocalUsageAnchorProducersNeverMutateCachedSliceInPlace(t *testing.T) {
	dataDir := t.TempDir()
	s := New(Config{DataDir: dataDir})
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"target":"node:9","session_callback":{"node_id":9}}`}); err != nil {
		t.Fatal(err)
	}
	fixture := steadyStateLegacyFixture(48, 8, 4)
	if err := s.persistLocalUsageDeliveriesLocked(fixture); err != nil {
		t.Fatal(err)
	}
	s.localUsageDeliveries = fixture
	s.localUsageDeliveryIndex = nil
	s.xrayUsageLoaded = true

	// Publish the anchor cache for this exact slice.
	if _, _, err := s.anchorSidecarLocked(fixture); err != nil {
		t.Fatal(err)
	}

	// Deep snapshot of the retained slice before any producer runs.
	before := deepCopyAnchorRows(t, fixture)

	for i := 0; i < 40; i++ {
		batchID := fmt.Sprintf("xray-anchor-immut-%d", i)
		s.xrayUsageMu.Lock()
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: batchID, Samples: []xrayUsageSample{{UserID: int64(i%8 + 1), InboundTag: "inbound-0", Value: uint64(i + 1)}}}
		s.xrayUsageMu.Unlock()
		batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{ReflectionVersion: 2})
		if err != nil {
			t.Fatalf("collect %s: %v", batchID, err)
		}
		if _, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()}); err != nil {
			t.Fatalf("ack %s: %v", batchID, err)
		}
	}

	// The producers may have replaced the published anchor set, but they must
	// not have edited the rows of the slice the cache was keyed on.
	after := deepCopyAnchorRows(t, fixture)
	if len(after) != len(before) {
		t.Fatalf("producer changed the retained anchor slice length: got %d want %d", len(after), len(before))
	}
	for i := range before {
		if after[i].BatchID != before[i].BatchID || len(after[i].Rows) != len(before[i].Rows) {
			t.Fatalf("producer changed retained anchor %d identity", i)
		}
		for j := range before[i].Rows {
			if after[i].Rows[j] != before[i].Rows[j] {
				t.Fatalf("producer mutated retained anchor row %d/%d in place: got %+v want %+v", i, j, after[i].Rows[j], before[i].Rows[j])
			}
		}
	}

	// And the cache must still describe the retained slice, not a replacement.
	raw, hash, err := s.anchorSidecarLocked(fixture)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, wantHash := referenceAnchorSidecar(t, fixture)
	if string(raw) != string(wantRaw) || hash != wantHash {
		t.Fatalf("anchor cache diverged from retained slice: hash=%s want=%s", hash, wantHash)
	}
}

// deepCopyAnchorRows returns an independent copy of the anchor rows so a test
// can compare contents before and after producers run.
func deepCopyAnchorRows(t *testing.T, deliveries []localUsageDelivery) []localUsageDelivery {
	t.Helper()
	out := make([]localUsageDelivery, len(deliveries))
	for i, delivery := range deliveries {
		out[i] = delivery
		out[i].Rows = append([]localUsageDeliveryRow(nil), delivery.Rows...)
	}
	return out
}
