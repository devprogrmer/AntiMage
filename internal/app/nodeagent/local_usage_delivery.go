package nodeagent

import (
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
	if state.Version != 1 || len(state.Deliveries) > 4096 {
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

func (s *Server) localAwaitingReflectionUsage(protocol string, userID int64, inboundTag, pendingChildID, reflectedRootBatchID string) (uint64, error) {
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return 0, err
	}
	reflectedThrough := -1
	for index, delivery := range s.localUsageDeliveries {
		if delivery.BatchID == reflectedRootBatchID {
			reflectedThrough = index
			break
		}
	}
	var total uint64
	changed := false
	next := make([]localUsageDelivery, 0, len(s.localUsageDeliveries))
	for index, delivery := range s.localUsageDeliveries {
		rows := make([]localUsageDeliveryRow, 0, len(delivery.Rows))
		for _, row := range delivery.Rows {
			if index < reflectedThrough && row.UserID == userID {
				changed = true
				continue
			}
			if index == reflectedThrough && row.UserID == userID && row.Raw != 0 {
				row.Raw = 0
				changed = true
			}
			if index == reflectedThrough && row.UserID == userID && row.Protocol == protocol && row.ChildID != pendingChildID {
				changed = true
				continue
			}
			rows = append(rows, row)
			if row.Protocol != protocol || row.UserID != userID || row.InboundTag != inboundTag || row.ChildID == pendingChildID {
				continue
			}
			if ^uint64(0)-total < row.Raw {
				return 0, fmt.Errorf("unreflected accounting usage overflow")
			}
			total += row.Raw
		}
		if len(rows) > 0 {
			next = append(next, localUsageDelivery{BatchID: delivery.BatchID, Rows: rows})
		}
	}
	if changed {
		if err := s.persistLocalUsageDeliveriesLocked(next); err != nil {
			return 0, err
		}
		s.localUsageDeliveries = next
	}
	return total, nil
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
	for _, delivery := range s.localUsageDeliveries {
		if delivery.BatchID != reflectedRootBatchID {
			continue
		}
		for _, row := range delivery.Rows {
			if row.UserID == userID && row.Protocol == protocol && row.ChildID == pendingChildID {
				return true, nil
			}
		}
	}
	return false, nil
}
