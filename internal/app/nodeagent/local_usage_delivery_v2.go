package nodeagent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

type localUsageOwnerKey struct {
	Protocol   string
	UserID     int64
	InboundTag string
}
type localUsageOwnerV2 struct {
	Protocol          string `json:"protocol"`
	UserID            int64  `json:"user_id"`
	InboundTag        string `json:"inbound_tag"`
	Total             uint64 `json:"total"`
	LegacyTotal       uint64 `json:"legacy_total"`
	RetiredLegacyRaw  uint64 `json:"retired_legacy_raw,omitempty"`
	RetiredThrough    uint64 `json:"retired_through,omitempty"`
	RetirementProofID string `json:"retirement_proof_id,omitempty"`
	LastChild         string `json:"last_child"`
	LastRaw           uint64 `json:"last_raw"`
	LastRoot          string `json:"last_root,omitempty"`
}
type localUsageReceiptV2 struct {
	Root     string           `json:"root"`
	Receipts map[int64]string `json:"receipts"`
}

// One aggregate per owner and one replay receipt per source family. Legacy
// anchors are a fixed migration input: no new history is ever appended to them.
// Deleted owners retain their aggregate as an accounting tombstone, not a
// global batch. Re-adding an owner must not forget its unreflected bytes.
type localUsageLedgerV2 struct {
	Epoch  string                         `json:"epoch"`
	Owners []localUsageOwnerV2            `json:"owners"`
	Latest map[string]localUsageReceiptV2 `json:"latest"`
}

type localUsageV2Index struct {
	Owners        map[localUsageOwnerKey]int
	Users         map[int64][]int
	LegacyBatches map[localUsageOwnerKey]map[string]uint64
	// UsageKeys caches usagereflection.Key for every owner identity. The
	// reflection hot path resolves the receipt key once per query per tick and
	// usagereflection.Key allocates on every call, so the value is computed
	// once per owner while the index is built or extended.
	UsageKeys map[localUsageOwnerKey]string
}

func firstReflectionLedger(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

// usageKeyForOwner encodes one owner identity as a cumulative receipt key.
func usageKeyForOwner(key localUsageOwnerKey) string {
	return usagereflection.Key(key.Protocol, key.InboundTag)
}

// usageKey returns the cached receipt key for an owner. The cache is filled
// while the index is built, but a lookup falls back to computing the key so an
// index assembled without the cache still resolves the same owner.
func (idx *localUsageV2Index) usageKey(key localUsageOwnerKey) string {
	if cached, ok := idx.UsageKeys[key]; ok {
		return cached
	}
	return usageKeyForOwner(key)
}

func usageRootFamily(root string) string { family, _, _ := strings.Cut(root, "-"); return family }
func validateLocalUsageV2(v *localUsageLedgerV2) error {
	if v == nil || v.Epoch == "" || v.Latest == nil || len(v.Latest) > 10 {
		return fmt.Errorf("invalid v2 accounting ledger")
	}
	seen := map[localUsageOwnerKey]bool{}
	for _, o := range v.Owners {
		key := localUsageOwnerKey{o.Protocol, o.UserID, o.InboundTag}
		retired := o.RetiredLegacyRaw > 0
		if o.UserID <= 0 || o.Protocol == "" || o.LegacyTotal > o.Total || o.LastRaw > o.Total || o.RetiredLegacyRaw > o.Total || o.RetiredThrough > o.Total || seen[key] ||
			(retired && (o.LegacyTotal != 0 || o.RetiredThrough < o.RetiredLegacyRaw || o.RetirementProofID == "")) ||
			(!retired && (o.RetiredThrough != 0 || o.RetirementProofID != "")) {
			return fmt.Errorf("invalid v2 accounting owner")
		}
		seen[key] = true
	}
	for family, r := range v.Latest {
		if family == "" || usageRootFamily(r.Root) != family {
			return fmt.Errorf("invalid v2 replay receipt")
		}
		for uid, raw := range r.Receipts {
			w, err := usagereflection.Decode(raw)
			if err != nil || uid <= 0 || w.Epoch != v.Epoch {
				return fmt.Errorf("invalid v2 replay watermark")
			}
		}
	}
	return nil
}
func (s *Server) localUsageV2IndexLocked() *localUsageV2Index {
	if s.localUsageV2Index != nil {
		return s.localUsageV2Index
	}
	idx := &localUsageV2Index{Owners: map[localUsageOwnerKey]int{}, Users: map[int64][]int{}, LegacyBatches: map[localUsageOwnerKey]map[string]uint64{}, UsageKeys: map[localUsageOwnerKey]string{}}
	for i, o := range s.localUsageV2.Owners {
		key := localUsageOwnerKey{o.Protocol, o.UserID, o.InboundTag}
		idx.Owners[key] = i
		idx.UsageKeys[key] = usageKeyForOwner(key)
		idx.Users[o.UserID] = append(idx.Users[o.UserID], i)
	}
	for _, delivery := range s.localUsageDeliveries {
		for _, row := range delivery.Rows {
			key := localUsageOwnerKey{row.Protocol, row.UserID, row.InboundTag}
			if idx.LegacyBatches[key] == nil {
				idx.LegacyBatches[key] = map[string]uint64{}
			}
			if ^uint64(0)-idx.LegacyBatches[key][delivery.BatchID] < row.Raw {
				// The loaded v1 row totals are validated again when a receipt is built.
				idx.LegacyBatches[key][delivery.BatchID] = ^uint64(0)
			} else {
				idx.LegacyBatches[key][delivery.BatchID] += row.Raw
			}
		}
	}
	s.localUsageV2Index = idx
	return idx
}
func (s *Server) migratedLocalUsageV2Locked() (*localUsageLedgerV2, error) {
	if s.localUsageV2 != nil {
		next := *s.localUsageV2
		next.Owners = append([]localUsageOwnerV2(nil), next.Owners...)
		next.Latest = make(map[string]localUsageReceiptV2, len(s.localUsageV2.Latest))
		for k, v := range s.localUsageV2.Latest {
			next.Latest[k] = v
		}
		return &next, nil
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, err
	}
	next := &localUsageLedgerV2{Epoch: hex.EncodeToString(epoch[:]), Latest: map[string]localUsageReceiptV2{}}
	index := map[localUsageOwnerKey]int{}
	for _, d := range s.localUsageDeliveries {
		for _, r := range d.Rows {
			key := localUsageOwnerKey{r.Protocol, r.UserID, r.InboundTag}
			pos, ok := index[key]
			if !ok {
				pos = len(next.Owners)
				index[key] = pos
				next.Owners = append(next.Owners, localUsageOwnerV2{Protocol: r.Protocol, UserID: r.UserID, InboundTag: r.InboundTag})
			}
			o := &next.Owners[pos]
			if ^uint64(0)-o.Total < r.Raw {
				return nil, fmt.Errorf("migration accounting overflow")
			}
			o.Total += r.Raw
			o.LegacyTotal = o.Total
			if o.LastChild != r.ChildID {
				o.LastRaw = 0
			}
			if ^uint64(0)-o.LastRaw < r.Raw {
				return nil, fmt.Errorf("migration child overflow")
			}
			o.LastRaw += r.Raw
			o.LastChild = r.ChildID
			o.LastRoot = d.BatchID
		}
	}
	return next, nil
}

func (s *Server) recordLocalUsageDeliveryV2(root string) (map[int64]string, error) {
	s.runtimeMetricsCounters.deliveryCalls.Add(1)
	if root == "" {
		return nil, nil
	}
	// Snapshot before the ledger lock: protocol checkpoint locks must never be
	// acquired while holding localUsageMu (quota callers use the reverse order).
	rows, err := s.snapshotUsageDeliveryRows(root)
	if err != nil {
		return nil, err
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return nil, err
	}
	family := usageRootFamily(root)
	if s.localUsageV2 != nil {
		if latest, ok := s.localUsageV2.Latest[family]; ok && latest.Root == root {
			replayed := map[localUsageOwnerKey]localUsageDeliveryRow{}
			for _, r := range rows {
				key := localUsageOwnerKey{r.Protocol, r.UserID, r.InboundTag}
				current, ok := replayed[key]
				if ok && current.ChildID != r.ChildID {
					return nil, fmt.Errorf("conflicting replay child identity")
				}
				if ^uint64(0)-current.Raw < r.Raw {
					return nil, fmt.Errorf("replayed usage overflow")
				}
				current.Protocol = r.Protocol
				current.UserID = r.UserID
				current.InboundTag = r.InboundTag
				current.ChildID = r.ChildID
				current.Raw += r.Raw
				replayed[key] = current
			}
			seen := map[localUsageOwnerKey]bool{}
			for key, r := range replayed {
				if r.Raw == 0 {
					continue
				}
				pos, exists := s.localUsageV2IndexLocked().Owners[key]
				if !exists {
					return nil, fmt.Errorf("conflicting replay of accounting owner")
				}
				owner := s.localUsageV2.Owners[pos]
				if owner.LastChild != r.ChildID || owner.LastRaw != r.Raw {
					return nil, fmt.Errorf("conflicting replay of accounting child")
				}
				seen[key] = true
			}
			expected := 0
			for _, encoded := range latest.Receipts {
				w, err := usagereflection.Decode(encoded)
				if err != nil {
					return nil, err
				}
				expected += len(w.Totals)
			}
			if len(seen) != expected {
				return nil, fmt.Errorf("conflicting replay of accounting receipt owners")
			}
			return cloneUsageReceipts(latest.Receipts), nil
		}
	}
	next, err := s.migratedLocalUsageV2Locked()
	if err != nil {
		return nil, err
	}
	legacyBatches := map[localUsageOwnerKey]map[string]uint64{}
	var index *localUsageV2Index
	if s.localUsageV2 != nil {
		index = s.localUsageV2IndexLocked()
		legacyBatches = index.LegacyBatches
	} else {
		for _, delivery := range s.localUsageDeliveries {
			for _, row := range delivery.Rows {
				key := localUsageOwnerKey{row.Protocol, row.UserID, row.InboundTag}
				if legacyBatches[key] == nil {
					legacyBatches[key] = map[string]uint64{}
				}
				if ^uint64(0)-legacyBatches[key][delivery.BatchID] < row.Raw {
					return nil, fmt.Errorf("legacy accounting batch overflow")
				}
				legacyBatches[key][delivery.BatchID] += row.Raw
			}
		}
		index = &localUsageV2Index{
			Owners:        make(map[localUsageOwnerKey]int, len(next.Owners)),
			Users:         make(map[int64][]int),
			LegacyBatches: legacyBatches,
			UsageKeys:     make(map[localUsageOwnerKey]string, len(next.Owners)),
		}
		for i, owner := range next.Owners {
			key := localUsageOwnerKey{owner.Protocol, owner.UserID, owner.InboundTag}
			index.Owners[key] = i
			index.UsageKeys[key] = usageKeyForOwner(key)
			index.Users[owner.UserID] = append(index.Users[owner.UserID], i)
		}
	}
	addedOwners := make(map[localUsageOwnerKey]int)
	// Aggregate duplicate samples before checking immutable child identity.
	grouped := map[localUsageOwnerKey]localUsageDeliveryRow{}
	for _, r := range rows {
		k := localUsageOwnerKey{r.Protocol, r.UserID, r.InboundTag}
		v, ok := grouped[k]
		if ok && v.ChildID != r.ChildID {
			return nil, fmt.Errorf("multiple pending children for accounting owner")
		}
		if ^uint64(0)-v.Raw < r.Raw {
			return nil, fmt.Errorf("accounting child overflow")
		}
		r.Raw += v.Raw
		grouped[k] = r
	}
	for key, r := range grouped {
		if r.Raw == 0 {
			continue
		}
		pos, ok := index.Owners[key]
		if !ok {
			pos = len(next.Owners)
			next.Owners = append(next.Owners, localUsageOwnerV2{Protocol: r.Protocol, UserID: r.UserID, InboundTag: r.InboundTag})
			addedOwners[key] = pos
		}
		o := &next.Owners[pos]
		if o.LastChild == r.ChildID {
			if o.LastRaw != r.Raw && o.LastRaw != 0 {
				return nil, fmt.Errorf("accounting child payload conflict")
			}
			continue
		}
		if ^uint64(0)-o.Total < r.Raw {
			return nil, fmt.Errorf("accounting owner overflow")
		}
		o.Total += r.Raw
		o.LastChild = r.ChildID
		o.LastRaw = r.Raw
		o.LastRoot = root
	}
	watermarks := map[int64]usagereflection.Watermark{}
	for key, r := range grouped {
		if r.Raw == 0 {
			continue
		}
		pos := index.Owners[key]
		if added, ok := addedOwners[key]; ok {
			pos = added
		}
		o := next.Owners[pos]
		w, ok := watermarks[r.UserID]
		if !ok {
			w = usagereflection.Watermark{Version: 2, Epoch: next.Epoch, Root: root, Totals: map[string]uint64{}, Baselines: map[string]uint64{}, LegacyRoots: map[string]string{}, LegacyBatches: map[string]map[string]uint64{}, PendingChildren: map[string]usagereflection.PendingChild{}}
		}
		// index.usageKey falls back to computing the key for an owner added in
		// this call, whose cache entry is published after the durable write.
		ownerKey := index.usageKey(key)
		w.Totals[ownerKey] = o.Total
		w.Baselines[ownerKey] = 0
		w.PendingChildren[ownerKey] = usagereflection.PendingChild{BatchID: r.ChildID, Raw: r.Raw}
		if o.LastRoot != "" {
			w.LegacyRoots[ownerKey] = o.LastRoot
		}
		if o.LegacyTotal > 0 {
			w.LegacyBatches[ownerKey] = cloneLegacyBatchTotals(legacyBatches[key])
		}
		watermarks[r.UserID] = w
	}
	receipts := map[int64]string{}
	for uid, w := range watermarks {
		raw, err := json.Marshal(w)
		if err != nil {
			return nil, err
		}
		receipts[uid] = string(raw)
	}
	next.Latest[family] = localUsageReceiptV2{Root: root, Receipts: receipts}
	old := s.localUsageV2
	s.localUsageV2 = next
	if err := s.persistLocalUsageDeliveriesLocked(s.localUsageDeliveries); err != nil {
		s.localUsageV2 = old
		return nil, err
	}
	// Publish new index positions only after the ledger is durable. Failed
	// writes must leave both the old ledger and its cached indexes intact.
	for key, pos := range addedOwners {
		index.Owners[key] = pos
		index.UsageKeys[key] = usageKeyForOwner(key)
		index.Users[key.UserID] = append(index.Users[key.UserID], pos)
	}
	s.localUsageV2Index = index
	return cloneUsageReceipts(receipts), nil
}
func cloneUsageReceipts(receipts map[int64]string) map[int64]string {
	copy := make(map[int64]string, len(receipts))
	for k, v := range receipts {
		copy[k] = v
	}
	return copy
}

func cloneLegacyBatchTotals(values map[string]uint64) map[string]uint64 {
	if len(values) == 0 {
		return nil
	}
	copyValues := make(map[string]uint64, len(values))
	for batch, raw := range values {
		copyValues[batch] = raw
	}
	return copyValues
}

func (s *Server) compactLegacyOwnersLocked(owners map[localUsageOwnerKey]bool) error {
	if len(owners) == 0 {
		return nil
	}
	oldLedger, oldDeliveries := s.localUsageV2, s.localUsageDeliveries
	nextLedger := *oldLedger
	nextLedger.Owners = append([]localUsageOwnerV2(nil), oldLedger.Owners...)
	idx := s.localUsageV2IndexLocked()
	for key := range owners {
		if pos, ok := idx.Owners[key]; ok {
			nextLedger.Owners[pos].LegacyTotal = 0
		}
	}
	nextDeliveries := make([]localUsageDelivery, 0, len(oldDeliveries))
	for _, delivery := range oldDeliveries {
		rows := make([]localUsageDeliveryRow, 0, len(delivery.Rows))
		for _, row := range delivery.Rows {
			if !owners[localUsageOwnerKey{row.Protocol, row.UserID, row.InboundTag}] {
				rows = append(rows, row)
			}
		}
		if len(rows) > 0 {
			delivery.Rows = rows
			nextDeliveries = append(nextDeliveries, delivery)
		}
	}
	s.localUsageV2 = &nextLedger
	if err := s.persistLocalUsageDeliveriesLocked(nextDeliveries); err != nil {
		s.localUsageV2 = oldLedger
		s.localUsageDeliveryIndex = nil
		return fmt.Errorf("compact reflected legacy accounting anchors: %w", err)
	}
	s.localUsageDeliveries = nextDeliveries
	s.localUsageV2Index = nil
	return nil
}

func (s *Server) localPendingReflectedV2Locked(protocol string, uid int64, child, raw string) (bool, error) {
	w, err := s.decodeLocalUsageReceiptLocked(raw)
	if err != nil {
		return false, err
	}
	if w.Epoch != s.localUsageV2.Epoch {
		return false, nil
	}
	matched := false
	idx := s.localUsageV2IndexLocked()
	for _, pos := range idx.Users[uid] {
		o := s.localUsageV2.Owners[pos]
		if o.Protocol != protocol || o.LastChild != child {
			continue
		}
		matched = true
		total, ok := w.Totals[idx.usageKey(localUsageOwnerKey{o.Protocol, o.UserID, o.InboundTag})]
		if !ok || total < o.Total {
			return false, nil
		}
		if total > o.Total {
			return false, fmt.Errorf("pending reflection exceeds durable accounting total")
		}
	}
	return matched, nil
}
func (s *Server) localUsageReflectionV2Locked(queries []localUsageReflectionQuery, results []localUsageReflectionResult) error {
	idx := s.localUsageV2IndexLocked()
	compactOwners := map[localUsageOwnerKey]bool{}
	for i, q := range queries {
		ownerKey := localUsageOwnerKey{q.Protocol, q.UserID, q.InboundTag}
		pos, ok := idx.Owners[ownerKey]
		if !ok {
			continue
		}
		// Resolved from the index cache so the receipt lookup does not allocate
		// per query on every tick.
		usageKey := idx.usageKey(ownerKey)
		o := s.localUsageV2.Owners[pos]
		var reflected uint64
		covered := false
		legacyRoot := q.ReflectedRootBatchID
		reflectedLedger := q.ReflectedLedger
		panelWatermark := reflectedLedger != ""
		if reflectedLedger == "" && q.ReflectedRootBatchID != "" {
			// A controller that predates reflection receipts still advances its
			// cursor by root batch id. Resolve that cursor against the durable
			// receipt for the same root so its acknowledged cumulative bytes are
			// not reported as unreflected forever. Unknown or stale roots keep the
			// conservative anchor-based fallback below.
			if latest, found := s.localUsageV2.Latest[usageRootFamily(q.ReflectedRootBatchID)]; found && latest.Root == q.ReflectedRootBatchID {
				reflectedLedger = latest.Receipts[q.UserID]
			}
		}
		if reflectedLedger != "" {
			// Decoded receipts are reused across ticks by the server-scoped
			// cache; the same user's receipt also recurs later in this batch.
			w, err := s.decodeLocalUsageReceiptLocked(reflectedLedger)
			if err != nil {
				return err
			}
			if w.Epoch == s.localUsageV2.Epoch {
				reflected, covered = w.Totals[usageKey]
				legacyRoot = w.LegacyRoot
				if reflected > o.Total {
					return fmt.Errorf("reflection exceeds durable accounting total")
				}
				// Anchor compaction requires the panel's own durable watermark.
				// A cursor recovered from the node's local receipt proves nothing
				// about panel persistence, so raw transport ACK must not retire
				// legacy anchors; only an explicit retirement proof may.
				if panelWatermark && o.LegacyTotal > 0 && reflected >= o.LegacyTotal {
					compactOwners[ownerKey] = true
				}
				results[i].PendingReflected = q.PendingChildID != "" && o.LastChild == q.PendingChildID && reflected >= o.Total
			}
		}
		if !covered {
			// Fixed migration anchors resolve old root cursors. Fresh v2 cursors use
			// cumulative watermarks and never traverse these historical rows.
			legacy := s.localUsageIndexLocked()
			through, found := legacy.Batches[legacyRoot]
			for _, entry := range legacy.Users[q.UserID] {
				r := entry.Row
				if r.Protocol != q.Protocol || r.InboundTag != q.InboundTag {
					continue
				}
				if found && entry.Position <= through {
					reflected += r.Raw
					if r.ChildID == q.PendingChildID && q.PendingChildID != "" {
						results[i].PendingReflected = true
					}
				}
			}
		}
		awaiting := o.Total - reflected
		if q.PendingChildID != "" && o.LastChild == q.PendingChildID && !results[i].PendingReflected {
			if o.LastRaw > awaiting {
				return fmt.Errorf("partial reflection of immutable pending child")
			}
			awaiting -= o.LastRaw
		}
		results[i].Awaiting = awaiting
	}
	if len(compactOwners) > 0 {
		if err := s.compactLegacyOwnersLocked(compactOwners); err != nil {
			return err
		}
	}
	return nil
}
