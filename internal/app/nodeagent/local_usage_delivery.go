package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const maxLocalUsageDeliveries = 4096

var errLocalUsageDeliveryCapacity = errors.New("unreflected accounting delivery capacity exceeded")

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
	Version    int                  `json:"version"`
	Deliveries []localUsageDelivery `json:"deliveries"`
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
	if state.Version != 1 || len(state.Deliveries) > maxLocalUsageDeliveries {
		return fmt.Errorf("invalid accounting delivery ledger version or capacity")
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
	s.localUsageDeliveries = state.Deliveries
	s.localUsageDeliveryIndex = nil
	s.localUsageLoaded = true
	return nil
}

func (s *Server) persistLocalUsageDeliveriesLocked(deliveries []localUsageDelivery) error {
	path := filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(localUsageDeliveryState{Version: 1, Deliveries: deliveries})
	if err != nil {
		return err
	}
	return writeAccountingState(path, raw)
}

func usageDeliveryRows(protocol, childID string, batch *nodev1.UserUsageBatch) ([]localUsageDeliveryRow, error) {
	if batch.GetBatchId() != childID {
		return nil, fmt.Errorf("accounting delivery child changed: %s", childID)
	}
	rows := make([]localUsageDeliveryRow, 0, len(batch.GetStats()))
	for _, sample := range batch.GetStats() {
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
	return s.appendLocalUsageDelivery(localUsageDelivery{BatchID: batchID, Rows: rows})
}

func (s *Server) appendLocalUsageDelivery(delivery localUsageDelivery) error {
	s.localUsageMu.Lock()
	if len(s.localUsageDeliveries) < maxLocalUsageDeliveries {
		defer s.localUsageMu.Unlock()
		return s.commitLocalUsageDeliveryLocked(s.localUsageDeliveries, delivery)
	}
	s.localUsageMu.Unlock()

	// Policy applies and ledger reclamation must agree on the committed quota
	// snapshot. Never hold localUsageMu while waiting for a policy transition.
	s.xrayTransitionMu.Lock()
	defer s.xrayTransitionMu.Unlock()
	snapshot, err := s.currentRuntimePolicy()
	if err != nil {
		return fmt.Errorf("load policy for accounting delivery recovery: %w", err)
	}

	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	deliveries := s.localUsageDeliveries
	for _, existing := range deliveries {
		if existing.BatchID == delivery.BatchID {
			return nil
		}
	}
	if len(deliveries) >= maxLocalUsageDeliveries && snapshot != nil && !snapshot.Stopped {
		payload, err := snapshot.nativePayload()
		if err != nil {
			return fmt.Errorf("read policy for accounting delivery recovery: %w", err)
		}
		deliveries = s.reclaimReflectedDeliveriesLocked(payload)
	}
	return s.commitLocalUsageDeliveryLocked(deliveries, delivery)
}

func (s *Server) commitLocalUsageDeliveryLocked(deliveries []localUsageDelivery, delivery localUsageDelivery) error {
	for _, existing := range deliveries {
		if existing.BatchID == delivery.BatchID {
			return nil
		}
	}
	if len(deliveries) >= maxLocalUsageDeliveries {
		return errLocalUsageDeliveryCapacity
	}
	next := make([]localUsageDelivery, len(deliveries), len(deliveries)+1)
	copy(next, deliveries)
	next = append(next, delivery)
	if err := s.persistLocalUsageDeliveriesLocked(next); err != nil {
		return err
	}
	s.localUsageDeliveries = next
	s.localUsageDeliveryIndex = nil
	return nil
}

// Retain every marker batch, including its child identities for lost ACK
// recovery. A batch can disappear only when all of its rows precede an
// applicable marker. Absent markers and conflicting policy views are treated
// conservatively.
func (s *Server) reclaimReflectedDeliveriesLocked(payload nativeRuntimePayload) []localUsageDelivery {
	positions := s.localUsageIndexLocked().Batches
	through := make(map[int64]int)
	anchors := make(map[string]bool)
	add := func(userID int64, marker string) {
		position, found := positions[marker]
		if found {
			anchors[marker] = true
		} else {
			position = -1
		}
		if previous, seen := through[userID]; !seen || position < previous {
			through[userID] = position
		}
	}
	for _, user := range payload.XrayPolicies {
		add(user.UserID, user.ReflectedUsageBatchID)
	}
	for _, inbound := range payload.OpenVPNInbounds {
		for _, user := range inbound.Users {
			add(user.UserID, user.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.L2TPInbounds {
		for _, user := range inbound.Users {
			add(user.UserID, user.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.PPTPInbounds {
		for _, user := range inbound.Users {
			add(user.UserID, user.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.WireGuardInbounds {
		for _, peer := range inbound.Peers {
			add(peer.UserID, peer.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.AmneziaWGInbounds {
		for _, peer := range inbound.Peers {
			add(peer.UserID, peer.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.IKEv2Inbounds {
		for _, user := range inbound.Users {
			add(user.UserID, user.ReflectedUsageBatchID)
		}
	}
	for _, inbound := range payload.AnyConnectInbounds {
		for _, user := range inbound.Users {
			add(user.UserID, user.ReflectedUsageBatchID)
		}
	}

	type protocolOwner struct {
		protocol string
		userID   int64
	}
	latest := make(map[protocolOwner]string)
	for _, delivery := range s.localUsageDeliveries {
		for _, row := range delivery.Rows {
			latest[protocolOwner{row.Protocol, row.UserID}] = delivery.BatchID
		}
	}
	for _, marker := range payload.WireGuardAccountingRefreshed {
		anchors[marker] = true
	}
	for _, marker := range latest {
		anchors[marker] = true
	}

	kept := make([]localUsageDelivery, 0, len(s.localUsageDeliveries))
	for index, delivery := range s.localUsageDeliveries {
		keep := anchors[delivery.BatchID] || len(delivery.Rows) == 0
		for _, row := range delivery.Rows {
			if index >= through[row.UserID] {
				keep = true
				break
			}
		}
		if keep {
			kept = append(kept, delivery)
		}
	}
	return kept
}

func (s *Server) localAwaitingReflectionUsage(protocol string, userID int64, inboundTag, pendingChildID, reflectedRootBatchID string) (uint64, error) {
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return 0, err
	}
	// Readers can run in helper subprocesses. Keep quota evaluation read-only
	// and preserve batch/child identities until the committed policy permits
	// reclaiming the whole delivery.
	return s.localUsageReflectionIndexedLocked(protocol, userID, inboundTag, pendingChildID, reflectedRootBatchID)
}

func (s *Server) localPendingUsageReflected(protocol string, userID int64, pendingChildID, reflectedRootBatchID string) (bool, error) {
	if pendingChildID == "" || reflectedRootBatchID == "" {
		return false, nil
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return false, err
	}
	index := s.localUsageIndexLocked()
	position, found := index.Batches[reflectedRootBatchID]
	if !found {
		return false, nil
	}
	for _, entry := range index.Users[userID] {
		row := s.localUsageDeliveries[entry.Position].Rows[entry.RowPosition]
		if entry.Position <= position && row.Protocol == protocol && row.ChildID == pendingChildID {
			return true, nil
		}
	}
	return false, nil
}
