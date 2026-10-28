package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type localUsageDeliveryRow struct {
	Protocol   string `json:"protocol"`
	ChildID    string `json:"child_id"`
	UserID     int64  `json:"user_id"`
	InboundTag string `json:"inbound_tag"`
	Raw        uint64 `json:"raw"`
}

type localUsageDelivery struct {
	BatchID string                  `json:"batch_id"`
	Rows    []localUsageDeliveryRow `json:"rows"`
}

type localUsageDeliveryState struct {
	LegacyAnchorsHash string               `json:"legacy_anchors_hash,omitempty"`
	Version           int                  `json:"version"`
	Deliveries        []localUsageDelivery `json:"deliveries"`
	Ledger            *localUsageLedgerV2  `json:"ledger,omitempty"`
}

func (s *Server) ensureLocalUsageDeliveriesLocked() error {
	if s.localUsageLoaded {
		return nil
	}
	raw, err := readOfflineAccountingState(filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json"))
	if os.IsNotExist(err) {
		s.localUsageLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var state localUsageDeliveryState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode accounting delivery ledger: %w", err)
	}
	if (state.Version != 1 && state.Version != 2) || len(state.Deliveries) > 4096 {
		return fmt.Errorf("invalid accounting delivery ledger version or capacity")
	}
	if state.Version == 2 && state.LegacyAnchorsHash != "" {
		if len(state.LegacyAnchorsHash) != 64 {
			return fmt.Errorf("invalid migration anchor identity")
		}
		if _, err := hex.DecodeString(state.LegacyAnchorsHash); err != nil {
			return fmt.Errorf("invalid migration anchor identity")
		}
		path := filepath.Join(s.cfg.DataDir, "accounting", "deliveries-v1-"+state.LegacyAnchorsHash+".json")
		raw, err := readOfflineAccountingState(path)
		if err != nil {
			return fmt.Errorf("read migration anchors: %w", err)
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != state.LegacyAnchorsHash {
			return fmt.Errorf("migration anchor checksum mismatch")
		}
		var anchors localUsageDeliveryState
		if err := json.Unmarshal(raw, &anchors); err != nil {
			return fmt.Errorf("decode migration anchors: %w", err)
		}
		if anchors.Version != 1 || len(anchors.Deliveries) > 4096 {
			return fmt.Errorf("invalid migration anchors")
		}
		state.Deliveries = anchors.Deliveries
	}
	if state.Version == 2 {
		if err := validateLocalUsageV2(state.Ledger); err != nil {
			return err
		}
		legacyTotals := map[localUsageOwnerKey]uint64{}
		for _, delivery := range state.Deliveries {
			for _, row := range delivery.Rows {
				key := localUsageOwnerKey{row.Protocol, row.UserID, row.InboundTag}
				if ^uint64(0)-legacyTotals[key] < row.Raw {
					return fmt.Errorf("migration anchor accounting overflow")
				}
				legacyTotals[key] += row.Raw
			}
		}
		for _, owner := range state.Ledger.Owners {
			key := localUsageOwnerKey{owner.Protocol, owner.UserID, owner.InboundTag}
			if legacyTotals[key] != owner.LegacyTotal {
				return fmt.Errorf("migration anchor total mismatch for user %d protocol %s inbound %s", owner.UserID, owner.Protocol, owner.InboundTag)
			}
			delete(legacyTotals, key)
		}
		if len(legacyTotals) != 0 {
			return fmt.Errorf("migration anchors contain an owner missing from cumulative ledger")
		}
	}
	seen := map[string]bool{}
	for _, delivery := range state.Deliveries {
		if delivery.BatchID == "" || seen[delivery.BatchID] {
			return fmt.Errorf("invalid accounting delivery batch identity")
		}
		seen[delivery.BatchID] = true
		for _, row := range delivery.Rows {
			if row.UserID <= 0 || row.ChildID == "" || row.Protocol == "" {
				return fmt.Errorf("invalid accounting delivery owner")
			}
		}
	}
	if state.Version == 2 {
		s.localUsageV2 = state.Ledger
	}
	s.localUsageDeliveries = state.Deliveries
	s.localUsageDeliveryIndex = nil
	s.localUsageLegacyHash = state.LegacyAnchorsHash
	s.localUsageLoaded = true
	return nil
}

func (s *Server) persistLocalUsageDeliveriesLocked(deliveries []localUsageDelivery) error {
	path := filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	state := localUsageDeliveryState{Version: 1, Deliveries: deliveries}
	previousAnchorHash := s.localUsageLegacyHash
	if s.localUsageV2 != nil {
		state.Version = 2
		state.Ledger = s.localUsageV2
		if len(deliveries) > 0 {
			raw, err := json.Marshal(localUsageDeliveryState{Version: 1, Deliveries: deliveries})
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			state.LegacyAnchorsHash = hex.EncodeToString(sum[:])
			if state.LegacyAnchorsHash != previousAnchorHash {
				anchorPath := filepath.Join(filepath.Dir(path), "deliveries-v1-"+state.LegacyAnchorsHash+".json")
				if err := writeAccountingState(anchorPath, raw); err != nil {
					return fmt.Errorf("persist migration anchors: %w", err)
				}
			}
		}
		state.Deliveries = nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeAccountingState(path, raw); err != nil {
		return err
	}
	s.localUsageLegacyHash = state.LegacyAnchorsHash
	s.localUsageDeliveryIndex = nil
	if previousAnchorHash != "" && previousAnchorHash != state.LegacyAnchorsHash {
		// The new content-addressed anchor is durable and referenced by the main
		// state before the old sidecar is removed.
		_ = os.Remove(filepath.Join(filepath.Dir(path), "deliveries-v1-"+previousAnchorHash+".json"))
	}
	return nil
}

func usageDeliveryRows(protocol, childID string, batch *nodev1.UserUsageBatch) ([]localUsageDeliveryRow, error) {
	if batch.GetBatchId() != childID {
		return nil, fmt.Errorf("accounting delivery child changed: %s", childID)
	}
	rows := make([]localUsageDeliveryRow, 0, len(batch.GetStats()))
	for _, sample := range batch.GetStats() {
		if sample.GetProtocol() != "" && sample.GetProtocol() != protocol {
			return nil, fmt.Errorf("accounting sample protocol mismatch: got %s want %s", sample.GetProtocol(), protocol)
		}
		if sample.GetChildBatchId() != "" && sample.GetChildBatchId() != childID {
			return nil, fmt.Errorf("accounting sample child mismatch: got %s want %s", sample.GetChildBatchId(), childID)
		}
		uid := strings.TrimPrefix(sample.GetUid(), "online:")
		if _, suffix, found := strings.Cut(uid, ":"); found {
			uid = suffix
		}
		if prefix, _, found := strings.Cut(uid, "."); found {
			uid = prefix
		}
		id, err := strconv.ParseInt(uid, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid accounting sample owner %q", sample.GetUid())
		}
		rows = append(rows, localUsageDeliveryRow{Protocol: protocol, ChildID: childID, UserID: id, InboundTag: sample.GetInboundTag(), Raw: sample.GetValue()})
	}
	return rows, nil
}

func (s *Server) snapshotUsageDeliveryRows(batchID string) ([]localUsageDeliveryRow, error) {
	if batchID == "" {
		return nil, nil
	}
	var batch *nodev1.UserUsageBatch
	protocol, _, _ := strings.Cut(batchID, "-")
	switch protocol {
	case "combined":
		s.combinedUsageMu.Lock()
		if err := s.ensureCombinedUsageStateLoadedLocked(); err != nil {
			s.combinedUsageMu.Unlock()
			return nil, err
		}
		pending := s.combinedUsagePending
		if pending == nil || pending.BatchID != batchID {
			s.combinedUsageMu.Unlock()
			return nil, fmt.Errorf("combined delivery does not match pending batch")
		}
		ids := []string{pending.CoreBatchID, pending.WireGuardBatchID, pending.L2TPBatchID, pending.PPTPBatchID, pending.AmneziaWGBatchID, pending.IKEv2BatchID, pending.AnyConnectBatchID}
		s.combinedUsageMu.Unlock()
		return s.snapshotUsageDeliveryChildren(ids)
	case "merged":
		s.mergedUsageMu.Lock()
		if err := s.ensureMergedUsageStateLoadedLocked(); err != nil {
			s.mergedUsageMu.Unlock()
			return nil, err
		}
		pending := s.mergedUsagePending
		if pending == nil || pending.MergedBatchID != batchID {
			s.mergedUsageMu.Unlock()
			return nil, fmt.Errorf("merged delivery does not match pending batch")
		}
		ids := []string{pending.OpenVPNBatchID, pending.XrayBatchID}
		s.mergedUsageMu.Unlock()
		return s.snapshotUsageDeliveryChildren(ids)
	case "xray":
		s.xrayUsageMu.Lock()
		defer s.xrayUsageMu.Unlock()
		if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.xrayUsagePending != nil {
			batch = xrayUsageBatchProto(s.xrayUsagePending)
		}
	case "wireguard":
		s.wireGuardUsageMu.Lock()
		defer s.wireGuardUsageMu.Unlock()
		if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.wireGuardUsagePending != nil {
			batch = wireGuardUsageBatchProto(s.wireGuardUsagePending, nil)
		}
	case "openvpn":
		s.openVPNUsageMu.Lock()
		defer s.openVPNUsageMu.Unlock()
		if err := s.ensureOpenVPNUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.openVPNUsagePending != nil {
			batch = openVPNUsageBatchProto(s.openVPNUsagePending)
		}
	case "l2tp":
		s.l2TPUsageMu.Lock()
		defer s.l2TPUsageMu.Unlock()
		if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.l2TPUsagePending != nil {
			batch = l2TPUsageBatchProto(s.l2TPUsagePending)
		}
	case "pptp":
		s.pptpUsageMu.Lock()
		defer s.pptpUsageMu.Unlock()
		if err := s.ensurePPTPUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.pptpUsagePending != nil {
			batch = pptpUsageBatchProto(s.pptpUsagePending)
		}
	case "ikev2":
		s.ikev2UsageMu.Lock()
		defer s.ikev2UsageMu.Unlock()
		if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.ikev2UsagePending != nil {
			batch = ikev2UsageBatchProto(s.ikev2UsagePending)
		}
	case "amneziawg":
		s.amneziaWGUsageMu.Lock()
		defer s.amneziaWGUsageMu.Unlock()
		if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.amneziaWGUsagePending != nil {
			batch = amneziaWGUsageBatchProto(s.amneziaWGUsagePending, nil)
		}
	case "anyconnect":
		s.anyConnectUsageMu.Lock()
		defer s.anyConnectUsageMu.Unlock()
		if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
			return nil, err
		}
		if s.anyConnectUsagePending != nil {
			batch = anyConnectUsageBatchProto(s.anyConnectUsagePending)
		}
	default:
		return nil, fmt.Errorf("unknown accounting delivery protocol %q", protocol)
	}
	return usageDeliveryRows(protocol, batchID, batch)
}

func (s *Server) snapshotUsageDeliveryChildren(ids []string) ([]localUsageDeliveryRow, error) {
	var rows []localUsageDeliveryRow
	for _, id := range ids {
		if id == "" {
			continue
		}
		childRows, err := s.snapshotUsageDeliveryRows(id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, childRows...)
	}
	return rows, nil
}

func (s *Server) recordLocalUsageDelivery(batchID string) error {
	if batchID == "" {
		return nil
	}
	s.localUsageMu.Lock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		s.localUsageMu.Unlock()
		return err
	}
	if s.localUsageV2 != nil {
		s.localUsageMu.Unlock()
		return fmt.Errorf("accounting ledger v2 requires a reflection-v2 collector; upgrade the panel before the node")
	}
	for _, delivery := range s.localUsageDeliveries {
		if delivery.BatchID == batchID {
			s.localUsageMu.Unlock()
			return nil
		}
	}
	s.localUsageMu.Unlock()
	rows, err := s.snapshotUsageDeliveryRows(batchID)
	if err != nil {
		return err
	}
	trafficRows := rows[:0]
	for _, row := range rows {
		if row.Raw > 0 {
			trafficRows = append(trafficRows, row)
		}
	}
	rows = trafficRows
	if len(rows) == 0 {
		return nil
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	for _, delivery := range s.localUsageDeliveries {
		if delivery.BatchID == batchID {
			return nil
		}
	}
	if len(s.localUsageDeliveries) >= 4096 {
		return fmt.Errorf("unreflected accounting delivery capacity exceeded")
	}
	next := append(append([]localUsageDelivery(nil), s.localUsageDeliveries...), localUsageDelivery{BatchID: batchID, Rows: rows})
	if err := s.persistLocalUsageDeliveriesLocked(next); err != nil {
		return err
	}
	s.localUsageDeliveries = next
	return nil
}

func (s *Server) localAwaitingReflectionUsage(protocol string, userID int64, inboundTag, pendingChildID, reflectedRootBatchID string, ledger ...string) (uint64, error) {
	results, err := s.localAwaitingReflectionUsageBatch([]localUsageReflectionQuery{{
		Protocol: protocol, UserID: userID, InboundTag: inboundTag,
		PendingChildID: pendingChildID, ReflectedRootBatchID: reflectedRootBatchID, ReflectedLedger: firstReflectionLedger(ledger),
	}})
	if err != nil {
		return 0, err
	}
	return results[0].Awaiting, nil
}

type localUsageReflectionQuery struct {
	Protocol             string
	UserID               int64
	InboundTag           string
	PendingChildID       string
	ReflectedRootBatchID string
	ReflectedLedger      string
}

type localUsageReflectionResult struct {
	Awaiting         uint64
	PendingReflected bool
}

// Batch several reflection checks under one lock and persist compaction once.
// Query order remains significant for users with multiple reflected roots.
func (s *Server) localAwaitingReflectionUsageBatch(queries []localUsageReflectionQuery) ([]localUsageReflectionResult, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	var results []localUsageReflectionResult
	if err := s.localAwaitingReflectionUsageBatchInto(queries, &results); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Server) localAwaitingReflectionUsageBatchInto(queries []localUsageReflectionQuery, results *[]localUsageReflectionResult) error {
	s.runtimeMetricsCounters.reflectionCalls.Add(1)
	if len(queries) == 0 {
		*results = (*results)[:0]
		return nil
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return err
	}
	if cap(*results) < len(queries) {
		*results = make([]localUsageReflectionResult, len(queries))
	} else {
		*results = (*results)[:len(queries)]
		clear(*results)
	}
	resultRows := *results
	if s.localUsageV2 != nil {
		return s.localUsageReflectionV2Locked(queries, resultRows)
	}
	if unchanged, err := s.localUsageReflectionIndexedLocked(queries, resultRows); unchanged || err != nil {
		return err
	}
	clear(resultRows)
	// Copy only when a new authoritative reflection actually requires compaction.
	deliveries := append([]localUsageDelivery(nil), s.localUsageDeliveries...)
	for index := range deliveries {
		deliveries[index].Rows = append([]localUsageDeliveryRow(nil), deliveries[index].Rows...)
	}
	changed := false
	for queryIndex, query := range queries {
		reflectedThrough := -1
		for index, delivery := range deliveries {
			if delivery.BatchID == query.ReflectedRootBatchID {
				reflectedThrough = index
				if query.PendingChildID != "" && query.ReflectedRootBatchID != "" {
					for _, row := range delivery.Rows {
						if row.UserID == query.UserID && row.Protocol == query.Protocol && row.ChildID == query.PendingChildID {
							resultRows[queryIndex].PendingReflected = true
							break
						}
					}
				}
				break
			}
		}
		queryChanged := false
		writeDelivery := 0
		for deliveryIndex := range deliveries {
			delivery := deliveries[deliveryIndex]
			writeRow := 0
			for _, row := range delivery.Rows {
				if deliveryIndex < reflectedThrough && row.UserID == query.UserID {
					queryChanged = true
					continue
				}
				if deliveryIndex == reflectedThrough && row.UserID == query.UserID && row.Raw != 0 {
					row.Raw = 0
					queryChanged = true
				}
				if deliveryIndex == reflectedThrough && row.UserID == query.UserID && row.Protocol == query.Protocol && row.ChildID != query.PendingChildID {
					queryChanged = true
					continue
				}
				delivery.Rows[writeRow] = row
				writeRow++
				if row.Protocol != query.Protocol || row.UserID != query.UserID || row.InboundTag != query.InboundTag || row.ChildID == query.PendingChildID {
					continue
				}
				if ^uint64(0)-resultRows[queryIndex].Awaiting < row.Raw {
					return fmt.Errorf("unreflected accounting usage overflow")
				}
				resultRows[queryIndex].Awaiting += row.Raw
			}
			delivery.Rows = delivery.Rows[:writeRow]
			if len(delivery.Rows) > 0 {
				deliveries[writeDelivery] = delivery
				writeDelivery++
			}
		}
		deliveries = deliveries[:writeDelivery]
		if queryChanged {
			changed = true
		}
	}
	if changed {
		if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
			return err
		}
		s.localUsageDeliveries = deliveries
	}
	return nil
}

func (s *Server) localPendingUsageReflected(protocol string, userID int64, pendingChildID, reflectedRootBatchID string, ledger ...string) (bool, error) {
	if pendingChildID == "" || (reflectedRootBatchID == "" && firstReflectionLedger(ledger) == "") {
		return false, nil
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return false, err
	}
	if s.localUsageV2 != nil && firstReflectionLedger(ledger) != "" {
		return s.localPendingReflectedV2Locked(protocol, userID, pendingChildID, firstReflectionLedger(ledger))
	}
	index := s.localUsageIndexLocked()
	position, found := index.Batches[reflectedRootBatchID]
	if !found {
		return false, nil
	}
	for _, entry := range index.Users[userID] {
		if entry.Position == position && entry.Row.Protocol == protocol && entry.Row.ChildID == pendingChildID {
			return true, nil
		}
	}

	return false, nil
}
