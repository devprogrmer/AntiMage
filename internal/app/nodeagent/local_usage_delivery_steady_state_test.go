package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/platform/usagereflection"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

var steadyStateProtocols = []string{"xray", "wireguard", "amneziawg", "openvpn", "l2tp", "pptp", "ikev2", "anyconnect"}

// steadyStateLegacyFixture builds a production-shaped v1 ledger: thousands of
// historical root batches across combined/merged/direct families, mixed users
// and protocols, including owners whose bytes are retained migration anchors.
func steadyStateLegacyFixture(roots, users, inbounds int) []localUsageDelivery {
	families := []string{"combined", "merged", "xray", "wireguard", "openvpn", "l2tp"}
	deliveries := make([]localUsageDelivery, 0, roots)
	for root := 0; root < roots; root++ {
		family := families[root%len(families)]
		delivery := localUsageDelivery{BatchID: fmt.Sprintf("%s-steady-%d", family, root)}
		rowCount := 1 + root%3
		for row := 0; row < rowCount; row++ {
			userID := int64((root*7+row*13)%users + 1)
			protocol := steadyStateProtocols[(root+row)%len(steadyStateProtocols)]
			if family == "xray" {
				protocol = "xray"
			}
			delivery.Rows = append(delivery.Rows, localUsageDeliveryRow{
				Protocol:   protocol,
				ChildID:    fmt.Sprintf("%s-child-%d-%d", protocol, root, row),
				UserID:     userID,
				InboundTag: fmt.Sprintf("inbound-%d", (root+row)%inbounds),
				Raw:        uint64(1000 + (root*31+row*17)%9000),
			})
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries
}

func fixtureBytes(deliveries []localUsageDelivery) uint64 {
	var total uint64
	for _, delivery := range deliveries {
		for _, row := range delivery.Rows {
			total += row.Raw
		}
	}
	return total
}

func fixtureRootIndex(deliveries []localUsageDelivery) map[localUsageOwnerKey]string {
	index := make(map[localUsageOwnerKey]string)
	for _, delivery := range deliveries {
		for _, row := range delivery.Rows {
			key := localUsageOwnerKey{row.Protocol, row.UserID, row.InboundTag}
			if _, exists := index[key]; !exists {
				index[key] = delivery.BatchID
			}
		}
	}
	return index
}

type steadyStateStats struct {
	maxLedgerBytes   int
	maxAnchorBytes   int
	maxLedgerEntries int
	collections      int
	restarts         int
	lostACKReplays   int
	compactions      int
	retiredAnchors   int
	proofReplays     int
	slowestCollect   time.Duration
}

// TestAccountingCapacityEliminatedUnderLongRunningSteadyState is the P0
// acceptance gate: run the real RPC path far past the historical 4096-root
// boundary, with restarts, lost ACKs, reflection, and byte conservation.
func TestAccountingCapacityEliminatedUnderLongRunningSteadyState(t *testing.T) {
	if testing.Short() {
		t.Skip("steady-state accounting acceptance runs long by design")
	}
	const (
		roots    = 4096
		users    = 96
		inbounds = 8
	)
	iterations := 12000
	if envIterations := os.Getenv("ANTIMAGE_STEADY_ITERATIONS"); envIterations != "" {
		// Local smoke runs can shrink the loop; CI runs the full 12000.
		var parsed int
		if _, err := fmt.Sscanf(envIterations, "%d", &parsed); err == nil && parsed > 0 {
			iterations = parsed
		}
	}

	dataDir := t.TempDir()
	fixture := steadyStateLegacyFixture(roots, users, inbounds)
	conserved := fixtureBytes(fixture)
	rootsByOwner := fixtureRootIndex(fixture)

	s := New(Config{DataDir: dataDir})
	s.localUsageLoaded = true
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"target":"node:7","session_callback":{"node_id":7}}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.persistLocalUsageDeliveriesLocked(fixture); err != nil {
		t.Fatal(err)
	}
	s.localUsageDeliveries = fixture
	s.localUsageDeliveryIndex = nil

	stats := steadyStateStats{}
	var results []localUsageReflectionResult
	previousAnchors := len(s.localUsageDeliveries)

	for i := 0; i < iterations; i++ {
		seededBytes := steadyStateSeed(t, s, i, users, inbounds)
		started := time.Now()
		batch, err := s.CollectUserUsage(context.Background(), &nodev1.CollectUsageRequest{ReflectionVersion: 2})
		if elapsed := time.Since(started); elapsed > stats.slowestCollect {
			stats.slowestCollect = elapsed
		}
		if err != nil {
			if strings.Contains(err.Error(), "unreflected accounting delivery capacity exceeded") {
				t.Fatalf("production capacity failure reproduced at iteration %d: %v", i, err)
			}
			t.Fatalf("collection %d failed: %v", i, err)
		}
		if batch.GetBatchId() == "" {
			t.Fatalf("collection %d produced no batch id", i)
		}
		batchRaw := batchRawBytes(batch)
		if batchRaw != seededBytes {
			t.Fatalf("collection %d delivered %d raw bytes, seeded %d", i, batchRaw, seededBytes)
		}
		stats.collections++
		conserved += batchRaw

		anchorBefore := len(s.localUsageDeliveries)
		if ack, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()}); err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK %d ack=%+v err=%v", i, ack, err)
		}
		if len(s.localUsageDeliveries) != anchorBefore {
			t.Fatalf("raw ACK retired a migration anchor at iteration %d", i)
		}
		// The fixture starts at the production shape: an already-full v1 ledger.
		// Each collection must therefore amortize into that fixed set instead of
		// appending a new anchor batch.
		if len(s.localUsageDeliveries) > roots {
			t.Fatalf("anchor sidecar grew past the production fixture at iteration %d: %d", i, len(s.localUsageDeliveries))
		}
		// Lost-ACK replay must be idempotent.
		billedBeforeReplay := localUsageV2TotalBytes(s)
		if ack, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.GetBatchId()}); err != nil || !ack.GetAcknowledged() {
			t.Fatalf("replayed ACK %d ack=%+v err=%v", i, ack, err)
		}
		if billedAfterReplay := localUsageV2TotalBytes(s); billedAfterReplay != billedBeforeReplay {
			t.Fatalf("lost-ACK replay %d double billed: cumulative=%d want=%d", i, billedAfterReplay, billedBeforeReplay)
		}
		if len(s.localUsageDeliveries) != anchorBefore {
			t.Fatalf("lost-ACK replay retired a migration anchor at iteration %d", i)
		}
		stats.lostACKReplays++

		// Real reflection path with legacy root cursors and cumulative receipts.
		if i%50 == 0 {
			queries := steadyStateMixedQueries(s, users, inbounds, rootsByOwner)
			if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
				t.Fatalf("reflection pass %d failed: %v", i, err)
			}
			if len(s.localUsageDeliveries) < previousAnchors {
				stats.compactions++
				previousAnchors = len(s.localUsageDeliveries)
			}
		}
		if i%100 == 0 {
			stats.maxLedgerBytes = max(stats.maxLedgerBytes, measureAccountingBytes(t, dataDir, &stats.maxAnchorBytes))
			if s.localUsageV2 != nil {
				stats.maxLedgerEntries = max(stats.maxLedgerEntries, len(s.localUsageV2.Owners))
			}
		}

		if i%2000 == 1999 {
			billedBeforeRestart := localUsageV2TotalBytes(s)
			restarted := New(Config{DataDir: dataDir})
			if err := restarted.ensureLocalUsageDeliveriesLocked(); err != nil {
				t.Fatalf("restart %d load failed: %v", i, err)
			}
			if billedAfterRestart := localUsageV2TotalBytes(restarted); billedAfterRestart != billedBeforeRestart {
				t.Fatalf("restart %d lost or duplicated billing: cumulative=%d want=%d", i, billedAfterRestart, billedBeforeRestart)
			}
			if err := restarted.localAwaitingReflectionUsageBatchInto(steadyStateMixedQueries(restarted, users, inbounds, rootsByOwner), &results); err != nil {
				t.Fatalf("restart %d reflection failed: %v", i, err)
			}
			if restarted.localUsageV2 == nil {
				t.Fatalf("restart %d lost the migrated cumulative ledger", i)
			}
			if billedAfterRestartReflection := localUsageV2TotalBytes(restarted); billedAfterRestartReflection != billedBeforeRestart {
				t.Fatalf("restart %d reflection re-billed after replay: cumulative=%d want=%d", i, billedAfterRestartReflection, billedBeforeRestart)
			}
			if len(restarted.localUsageDeliveries) > roots {
				t.Fatalf("restart %d anchor sidecar grew beyond the fixture: %d", i, len(restarted.localUsageDeliveries))
			}
			if len(restarted.localUsageDeliveries) < previousAnchors {
				stats.compactions++
			}
			previousAnchors = len(restarted.localUsageDeliveries)
			s = restarted
			stats.restarts++
		}

		// Retire one soft-deleted owner with the real durable-proof RPC. Stale,
		// mismatched, and cross-node proofs must leave anchors intact.
		if i > 0 && i%1500 == 0 {
			retired, replayed := steadyStateRetireOwner(t, s, i, rootsByOwner)
			stats.retiredAnchors += retired
			stats.proofReplays += replayed
		}
	}

	stats.maxLedgerBytes = max(stats.maxLedgerBytes, measureAccountingBytes(t, dataDir, &stats.maxAnchorBytes))
	if s.localUsageV2 == nil {
		t.Fatal("steady state never migrated to the bounded cumulative ledger")
	}
	stats.maxLedgerEntries = max(stats.maxLedgerEntries, len(s.localUsageV2.Owners))

	// The historical failure appeared once the delivery ledger pinned 4096
	// batches. A full acceptance run must clear that boundary with margin and
	// exercise at least one restart cycle. Short local smoke runs opt out of the
	// boundary requirement but still exercise every other assertion.
	if iterations > 4096 {
		if stats.collections <= 4096 {
			t.Fatalf("steady state did not clear the historical 4096 boundary: %d collections", stats.collections)
		}
		if stats.restarts == 0 {
			t.Fatal("steady state never included a restart cycle")
		}
	}

	if observed := observedOwnerBytes(s, users, inbounds); observed != conserved {
		t.Fatalf("byte conservation failed: observed=%d want=%d", observed, conserved)
	}

	// Bounded state: no per-collection growth in the durable ledger or anchors.
	if stats.maxLedgerEntries > users*len(steadyStateProtocols)*inbounds {
		t.Fatalf("owner ledger is not bounded: %d entries", stats.maxLedgerEntries)
	}
	if len(s.localUsageDeliveries) > roots {
		t.Fatalf("anchor sidecar is not bounded: %d entries", len(s.localUsageDeliveries))
	}
	if stats.maxLedgerBytes > 8<<20 {
		t.Fatalf("durable ledger is not bounded: %d bytes", stats.maxLedgerBytes)
	}
	if stats.maxAnchorBytes > maxOfflineAccountingBytes {
		t.Fatalf("anchor sidecar exceeds safe capacity: %d bytes", stats.maxAnchorBytes)
	}

	t.Logf("steady-state: collections=%d restarts=%d lost_ack_replays=%d proof_replays=%d compactions=%d retired_anchors=%d max_ledger_entries=%d max_ledger_bytes=%d max_anchor_bytes=%d conserved_bytes=%d anchors_remaining=%d",
		stats.collections, stats.restarts, stats.lostACKReplays, stats.proofReplays, stats.compactions, stats.retiredAnchors, stats.maxLedgerEntries, stats.maxLedgerBytes, stats.maxAnchorBytes, conserved, len(s.localUsageDeliveries))
	t.Logf("steady-state timing: slowest_collection=%s", stats.slowestCollect)
	t.Logf("steady-state reflection benchmark: %s", benchmarkReflectionAllocs(s, users, inbounds, rootsByOwner))
}

func benchmarkReflectionAllocs(s *Server, users, inbounds int, rootsByOwner map[localUsageOwnerKey]string) string {
	queries := steadyStateMixedQueries(s, users, inbounds, rootsByOwner)
	var results []localUsageReflectionResult
	if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		return "error: " + err.Error()
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
			panic(err)
		}
	})
	return fmt.Sprintf("queries=%d allocs_per_pass=%.0f", len(queries), allocs)
}

func steadyStateSeed(t *testing.T, s *Server, i, users, inbounds int) uint64 {
	t.Helper()
	userID := int64(i%users + 1)
	tag := fmt.Sprintf("inbound-%d", i%inbounds)
	raw := uint64(500 + i%1500)

	s.xrayUsageMu.Lock()
	s.xrayUsagePending = nil
	s.xrayUsageMu.Unlock()
	s.wireGuardUsageMu.Lock()
	s.wireGuardUsagePending = nil
	s.wireGuardUsageMu.Unlock()
	s.openVPNUsageMu.Lock()
	s.openVPNUsagePending = nil
	s.openVPNUsageMu.Unlock()
	s.mergedUsageMu.Lock()
	s.mergedUsagePending = nil
	s.mergedUsageMu.Unlock()
	s.combinedUsageMu.Lock()
	s.combinedUsagePending = nil
	s.combinedUsageMu.Unlock()

	switch i % 3 {
	case 0:
		batchID := fmt.Sprintf("xray-steady-run-%d", i)
		s.xrayUsageLoaded = true
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: batchID, Samples: []xrayUsageSample{{UserID: userID, InboundTag: tag, Value: raw}}}
		if err := s.persistXrayUsageStateLocked(); err != nil {
			t.Fatal(err)
		}
		return raw
	case 1:
		batchID := fmt.Sprintf("wireguard-steady-run-%d", i)
		s.wireGuardUsageLoaded = true
		s.wireGuardUsagePending = &wireGuardUsagePendingBatch{BatchID: batchID, Samples: []wireGuardUsageSample{{UserID: userID, InboundTag: tag, Value: raw}}}
		if err := s.persistWireGuardUsageStateLocked(); err != nil {
			t.Fatal(err)
		}
		return raw
	default:
		ovpnID := fmt.Sprintf("openvpn-steady-run-%d", i)
		s.openVPNUsageLoaded = true
		s.openVPNUsagePending = &openVPNUsagePendingBatch{BatchID: ovpnID, Samples: []openVPNUsageSample{{UserID: userID, InboundTag: tag, Value: raw}}}
		if err := s.persistOpenVPNUsageStateLocked(); err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func batchRawBytes(batch *nodev1.UserUsageBatch) uint64 {
	var total uint64
	for _, sample := range batch.GetStats() {
		total += sample.GetValue()
	}
	return total
}

// steadyStateOwnerRows counts the anchor rows that belong to one owner without
// assuming a one-owner-per-batch shape: combined/merged deliveries carry rows
// for many owners at once.
func steadyStateOwnerRows(s *Server, key localUsageOwnerKey) int {
	rows := 0
	for _, delivery := range s.localUsageDeliveries {
		for _, row := range delivery.Rows {
			if row.Protocol == key.Protocol && row.UserID == key.UserID && row.InboundTag == key.InboundTag {
				rows++
			}
		}
	}
	return rows
}

// steadyStateRetireOwner exercises the durable retirement RPC against one
// already-soft-deleted owner. It proves stale and mismatched proofs preserve the
// anchor, and that an exact committed proof retires it idempotently.
func steadyStateRetireOwner(t *testing.T, s *Server, seed int, rootsByOwner map[localUsageOwnerKey]string) (int, int) {
	t.Helper()
	if s.localUsageV2 == nil {
		return 0, 0
	}
	index := s.localUsageV2IndexLocked()
	var chosen localUsageOwnerKey
	var owner localUsageOwnerV2
	found := false
	for key, pos := range index.Owners {
		candidate := s.localUsageV2.Owners[pos]
		if candidate.LegacyTotal > 0 && candidate.RetiredLegacyRaw == 0 {
			chosen, owner, found = key, candidate, true
			break
		}
	}
	if !found {
		return 0, 0
	}
	nodeID, err := s.currentAccountingNodeID()
	if err != nil {
		return 0, 0
	}
	ownerKey := usagereflection.Key(chosen.Protocol, chosen.InboundTag)
	proof := func(total, legacy uint64) *nodev1.AccountingRetirementProof {
		p := &nodev1.AccountingRetirementProof{
			NodeId: nodeID, UserId: owner.UserID, Epoch: s.localUsageV2.Epoch,
			OwnerKey: ownerKey, TotalRaw: total, LegacyRaw: legacy,
		}
		p.ProofId = usagereflection.RetirementProofID(p.NodeId, p.UserId, p.Epoch, p.OwnerKey, p.TotalRaw, p.LegacyRaw)
		return p
	}
	anchorsBefore := len(s.localUsageDeliveries)
	ownerRowsBefore := steadyStateOwnerRows(s, chosen)

	// Stale cumulative total must not retire the anchor.
	if _, err := s.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof(owner.Total-1, owner.LegacyTotal)}}); err == nil {
		t.Fatalf("steady state %d: stale proof retired an anchor", seed)
	}
	// Mismatched legacy bytes must not retire the anchor.
	if _, err := s.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof(owner.Total, owner.LegacyTotal+1)}}); err == nil {
		t.Fatalf("steady state %d: mismatched legacy bytes retired an anchor", seed)
	}
	if len(s.localUsageDeliveries) != anchorsBefore {
		t.Fatalf("steady state %d: invalid proofs altered anchor batches", seed)
	}
	if rows := steadyStateOwnerRows(s, chosen); rows != ownerRowsBefore {
		t.Fatalf("steady state %d: invalid proofs altered owner anchor rows: %d want %d", seed, rows, ownerRowsBefore)
	}

	// Exact committed proof retires exactly this owner, idempotently.
	response, err := s.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof(owner.Total, owner.LegacyTotal)}})
	if err != nil || !response.GetAcknowledged() {
		t.Fatalf("steady state %d: valid proof rejected: %+v err=%v", seed, response, err)
	}
	if rows := steadyStateOwnerRows(s, chosen); rows != 0 {
		t.Fatalf("steady state %d: valid proof left %d anchor rows for the retired owner", seed, rows)
	}
	replay, err := s.RetireAccountingAnchors(context.Background(), &nodev1.RetireAccountingAnchorsRequest{Proofs: []*nodev1.AccountingRetirementProof{proof(owner.Total, owner.LegacyTotal), proof(owner.Total, owner.LegacyTotal)}})
	if err != nil || !replay.GetAcknowledged() {
		t.Fatalf("steady state %d: proof replay failed: %+v err=%v", seed, replay, err)
	}
	pos, ok := s.localUsageV2IndexLocked().Owners[chosen]
	if !ok {
		t.Fatalf("steady state %d: retired owner disappeared from the cumulative ledger", seed)
	}
	if got := s.localUsageV2.Owners[pos]; got.Total != owner.Total || got.RetiredLegacyRaw != owner.LegacyTotal || got.LegacyTotal != 0 {
		t.Fatalf("steady state %d: retired owner state=%+v want total=%d legacy=%d", seed, got, owner.Total, owner.LegacyTotal)
	}
	_ = rootsByOwner
	return 1, 1
}

// steadyStateMixedQueries mixes three cursor shapes the production ledger has:
// legacy root-batch cursors, cumulative v2 receipts from the panel, and
// unreflected owners with no cursor at all.
func steadyStateMixedQueries(s *Server, users, inbounds int, rootsByOwner map[localUsageOwnerKey]string) []localUsageReflectionQuery {
	queries := make([]localUsageReflectionQuery, 0, users*inbounds*len(steadyStateProtocols))
	var ownerIndex *localUsageV2Index
	if s.localUsageV2 != nil {
		ownerIndex = s.localUsageV2IndexLocked()
	}
	for _, protocol := range steadyStateProtocols {
		for uid := int64(1); uid <= int64(users); uid++ {
			for tag := 0; tag < inbounds; tag++ {
				ownerKey := localUsageOwnerKey{protocol, uid, fmt.Sprintf("inbound-%d", tag)}
				query := localUsageReflectionQuery{
					Protocol: protocol, UserID: uid, InboundTag: ownerKey.InboundTag,
				}
				switch (uid + int64(tag)) % 3 {
				case 0:
					if ownerIndex != nil {
						if pos, ok := ownerIndex.Owners[ownerKey]; ok {
							owner := s.localUsageV2.Owners[pos]
							key := usagereflection.Key(protocol, query.InboundTag)
							if encoded, err := json.Marshal(usagereflection.Watermark{
								Version: 2, Epoch: s.localUsageV2.Epoch, Root: owner.LastRoot,
								Totals:    map[string]uint64{key: owner.Total},
								Baselines: map[string]uint64{key: owner.LegacyTotal},
							}); err == nil {
								query.ReflectedLedger = string(encoded)
							}
						}
					}
				case 1:
					query.ReflectedRootBatchID = rootsByOwner[ownerKey]
				}
				queries = append(queries, query)
			}
		}
	}
	return queries
}

func observedOwnerBytes(s *Server, users, inbounds int) uint64 {
	index := s.localUsageV2IndexLocked()
	var observed uint64
	for uid := int64(1); uid <= int64(users); uid++ {
		for _, pos := range index.Users[uid] {
			observed += s.localUsageV2.Owners[pos].Total
		}
	}
	return observed
}

// localUsageV2TotalBytes sums every owner's cumulative total. It covers the
// whole ledger, including owners whose user id falls outside the reflection
// query sweep, which is what a lost-ACK double-billing check needs.
func localUsageV2TotalBytes(s *Server) uint64 {
	if s.localUsageV2 == nil {
		return 0
	}
	var total uint64
	for i := range s.localUsageV2.Owners {
		total += s.localUsageV2.Owners[i].Total
	}
	return total
}

func measureAccountingBytes(t *testing.T, dataDir string, maxAnchor *int) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "accounting"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	var total, anchors int
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		size := int(info.Size())
		total += size
		if entry.Name() != "deliveries.json" {
			anchors += size
		}
	}
	*maxAnchor = max(*maxAnchor, anchors)
	return total
}
