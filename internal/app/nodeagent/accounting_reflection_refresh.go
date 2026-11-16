package nodeagent

import (
	"encoding/json"
	"maps"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// Refresh accounting without changing the applied runtime configuration. A
// user may advance only when every active protocol can use the new total.
func (s *Server) accountingReflectionRefreshNeeded() (bool, error) {
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return false, err
	}
	return len(s.localUsageDeliveries) >= maxLocalUsageDeliveries*3/4, nil
}

func (s *Server) applyAccountingReflections(proofs []*nodev1.AccountingReflection) error {
	if len(proofs) == 0 {
		return nil
	}
	s.xrayTransitionMu.Lock()
	defer s.xrayTransitionMu.Unlock()
	snapshot, err := s.currentRuntimePolicy()
	if err != nil || snapshot == nil || snapshot.Stopped {
		return err
	}
	payload, err := snapshot.nativePayload()
	if err != nil {
		return err
	}
	var native map[string]json.RawMessage
	if err := json.Unmarshal([]byte(snapshot.NativeJSON), &native); err != nil {
		return err
	}
	views, err := nativeAccountingViews(native)
	if err != nil {
		return err
	}
	s.nativeAccountingPolicyMu.Lock()
	defer s.nativeAccountingPolicyMu.Unlock()
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()
	if len(payload.WireGuardInbounds) != 0 {
		if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
			return err
		}
	}

	return withPPPAdmissionLock(s.cfg.DataDir, func() error {
		return s.persistAccountingReflections(snapshot, payload, native, views, proofs)
	})
}

func (s *Server) persistAccountingReflections(snapshot *runtimePolicySnapshot, payload nativeRuntimePayload, native map[string]json.RawMessage, views []accountingPolicyGroup, proofs []*nodev1.AccountingReflection) error {
	updates, wgThrough, err := s.selectAccountingReflections(views, proofs)
	if err != nil || len(updates) == 0 {
		return err
	}
	refreshed := maps.Clone(payload.WireGuardAccountingRefreshed)
	if refreshed == nil {
		refreshed = make(map[int64]string)
	}
	nativeReceipts := maps.Clone(payload.NativeAccountingRefreshed)
	if nativeReceipts == nil {
		nativeReceipts = make(map[int64]*nativeAccountingReceipt)
	}
	for uid, update := range updates {
		nativeReceipts[uid] = &nativeAccountingReceipt{BatchID: update.GetBatchId(), UsedTraffic: update.GetUsedTraffic(), Inbounds: make(map[string][]string)}
	}
	for _, group := range views {
		protocol := group.nativeProtocol()
		var tag string
		if group.container != nil {
			if err := json.Unmarshal(group.container["tag"], &tag); err != nil {
				return err
			}
		}
		changed := false
		for _, entry := range group.entries {
			receipt := updates[entry.UserID]
			if receipt == nil {
				continue
			}
			entry.raw["used_traffic"], _ = json.Marshal(receipt.GetUsedTraffic())
			entry.raw["reflected_usage_batch_id"], _ = json.Marshal(receipt.GetBatchId())
			if group.field == "wg_inbounds" {
				refreshed[entry.UserID] = wgThrough[entry.UserID]
			}
			if protocol != "" {
				proof := nativeReceipts[entry.UserID]
				proof.Inbounds[protocol] = append(proof.Inbounds[protocol], tag)
			}
			changed = true
		}
		if changed {
			entries, err := json.Marshal(group.rawEntries)
			if err != nil {
				return err
			}
			if group.container == nil {
				native[group.field] = entries
			} else {
				group.container[group.list] = entries
				native[group.field], err = json.Marshal(group.inbounds)
				if err != nil {
					return err
				}
			}
		}
	}
	native["wg_accounting_refreshed"], _ = json.Marshal(refreshed)
	native["accounting_refreshed"], _ = json.Marshal(nativeReceipts)
	raw, err := json.Marshal(native)
	if err != nil {
		return err
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson: snapshot.ConfigJSON, OvRuntimeJson: string(raw), DesiredRevision: snapshot.Revision,
	}); err != nil {
		return err
	}
	// The persisted policy must precede pruning, including across a crash.
	// Keep the current marker and the last child for each protocol/user to
	// cover a controller commit followed by a lost transport ACK.
	s.mu.Lock()
	committed := s.runtimePolicy
	s.mu.Unlock()
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	compacted := s.reclaimReflectedDeliveriesLocked(*committed.native)
	if len(compacted) == len(s.localUsageDeliveries) {
		return nil
	}
	if err := s.persistLocalUsageDeliveriesLocked(compacted); err != nil {
		return err
	}
	s.localUsageDeliveries = compacted
	s.localUsageDeliveryIndex = nil
	return nil
}

func (s *Server) selectAccountingReflections(views []accountingPolicyGroup, proofs []*nodev1.AccountingReflection) (map[int64]*nodev1.AccountingReflection, map[int64]string, error) {
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return nil, nil, err
	}
	index := s.localUsageIndexLocked()
	accepted := make(map[int64]*nodev1.AccountingReflection)
	for _, proof := range proofs {
		position, exists := index.Batches[proof.GetBatchId()]
		if !exists || proof.GetUsedTraffic() < 0 {
			continue
		}
		userID := proof.GetUserId()
		if prior, ok := accepted[userID]; ok && index.Batches[prior.GetBatchId()] >= position {
			continue
		}
		hasOwner := false
		for _, entry := range index.Users[userID] {
			hasOwner = hasOwner || entry.Position == position
		}
		if hasOwner {
			accepted[userID] = proof
		}
	}
	// Every enabled policy for this owner must advance together. For WireGuard
	// the separate ACK history must identify the reflected prefix as well.
	seen := make(map[int64]bool)
	check := func(id int64, currentMarker string, currentUsed int64) {
		seen[id] = true
		proof := accepted[id]
		if proof == nil {
			return
		}
		current, known := index.Batches[currentMarker]
		if proof.GetUsedTraffic() < currentUsed ||
			(currentMarker != "" && (!known || current >= index.Batches[proof.GetBatchId()])) {
			delete(accepted, id)
		}
	}
	for _, group := range views {
		for _, entry := range group.entries {
			check(entry.UserID, entry.ReflectedUsageBatchID, entry.UsedTraffic)
		}
	}
	wgThrough := make(map[int64]string)
	unproven := make(map[int64]bool)
	for _, batch := range s.wireGuardUsageAwaitingReflection {
		position, known := index.Batches[batch.BatchID]
		for _, sample := range batch.Samples {
			proof := accepted[sample.UserID]
			if proof == nil || sample.Value == 0 {
				continue
			}
			if !known {
				unproven[sample.UserID] = true
				continue
			}
			if position <= index.Batches[proof.GetBatchId()] {
				wgThrough[sample.UserID] = batch.BatchID
				// The WireGuard ledger is ordered by ACK. A known root
				// proves the prefix even if earlier roots were compacted.
				delete(unproven, sample.UserID)
			}
		}
	}
	if pending := s.wireGuardUsagePending; pending != nil {
		for _, sample := range pending.Samples {
			proof := accepted[sample.UserID]
			if sample.Value == 0 || proof == nil {
				continue
			}
			through := index.Batches[proof.GetBatchId()]
			known := false
			for _, entry := range index.Users[sample.UserID] {
				row := s.localUsageDeliveries[entry.Position].Rows[entry.RowPosition]
				if row.Protocol == "wireguard" && row.ChildID == pending.BatchID {
					known = true
					if entry.Position <= through {
						unproven[sample.UserID] = true
					}
				}
			}
			if !known {
				unproven[sample.UserID] = true
			}
		}
	}
	for id := range unproven {
		delete(accepted, id)
	}
	for id := range accepted {
		if !seen[id] {
			delete(accepted, id)
		}
	}
	return accepted, wgThrough, nil
}

type accountingPolicyEntry struct {
	UserID                int64  `json:"user_id"`
	UsedTraffic           int64  `json:"used_traffic"`
	ReflectedUsageBatchID string `json:"reflected_usage_batch_id"`
	raw                   map[string]json.RawMessage
}

type accountingPolicyGroup struct {
	field, list string
	container   map[string]json.RawMessage
	inbounds    []map[string]json.RawMessage
	entries     []accountingPolicyEntry
	rawEntries  []map[string]json.RawMessage
}

func nativeAccountingViews(native map[string]json.RawMessage) ([]accountingPolicyGroup, error) {
	var groups []accountingPolicyGroup
	add := func(group accountingPolicyGroup, raw json.RawMessage) error {
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, &group.rawEntries); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &group.entries); err != nil {
			return err
		}
		for i := range group.entries {
			group.entries[i].raw = group.rawEntries[i]
		}
		groups = append(groups, group)
		return nil
	}
	if err := add(accountingPolicyGroup{field: "xray_policies"}, native["xray_policies"]); err != nil {
		return nil, err
	}
	for _, field := range []string{"inbounds", "l2tp_inbounds", "pptp_inbounds", "wg_inbounds", "awg_inbounds", "ikev2_inbounds", "anyconnect_inbounds"} {
		if len(native[field]) == 0 {
			continue
		}
		var inbounds []map[string]json.RawMessage
		if err := json.Unmarshal(native[field], &inbounds); err != nil {
			return nil, err
		}
		list := "users"
		if field == "wg_inbounds" || field == "awg_inbounds" {
			list = "peers"
		}
		for _, inbound := range inbounds {
			if err := add(accountingPolicyGroup{field: field, list: list, container: inbound, inbounds: inbounds}, inbound[list]); err != nil {
				return nil, err
			}
		}
	}
	return groups, nil
}

func (s *Server) currentNativeAccountingPolicy(protocol string, uid int64, tag string, policy nativeSessionUserPolicy) (nativeSessionUserPolicy, error) {
	snapshot, err := s.currentRuntimePolicy()
	if err != nil || snapshot == nil || snapshot.Stopped {
		return policy, err
	}
	payload, err := snapshot.nativePayload()
	if err != nil {
		return policy, err
	}
	proof := payload.NativeAccountingRefreshed[uid]
	if proof == nil || !proof.inbounds[protocol][tag] {
		return policy, nil
	}
	policy.UsedTraffic = proof.UsedTraffic
	policy.ReflectedUsageBatchID = proof.BatchID
	return policy, nil
}

type nativeAccountingReceipt struct {
	BatchID     string              `json:"batch_id"`
	UsedTraffic int64               `json:"used_traffic"`
	Inbounds    map[string][]string `json:"inbounds"`
	inbounds    map[string]map[string]bool
}

func (p *nativeRuntimePayload) indexAccountingReceipts() {
	for _, receipt := range p.NativeAccountingRefreshed {
		if receipt == nil {
			continue
		}
		receipt.inbounds = make(map[string]map[string]bool)
		for protocol, tags := range receipt.Inbounds {
			receipt.inbounds[protocol] = make(map[string]bool, len(tags))
			for _, tag := range tags {
				receipt.inbounds[protocol][tag] = true
			}
		}
	}
}

func (g accountingPolicyGroup) nativeProtocol() string {
	switch g.field {
	case "inbounds":
		return "openvpn"
	case "awg_inbounds":
		return "amneziawg"
	case "xray_policies", "wg_inbounds":
		return ""
	default:
		return strings.TrimSuffix(g.field, "_inbounds")
	}
}

func (s *Server) withRefreshedWireGuardAccounting(cfg wireGuardUsageRuntimeConfig) wireGuardUsageRuntimeConfig {
	s.mu.Lock()
	snapshot := s.runtimePolicy
	s.mu.Unlock()
	if snapshot == nil || snapshot.Stopped || snapshot.native == nil ||
		len(snapshot.native.WireGuardAccountingRefreshed) == 0 {
		return cfg
	}
	copied := false
	for _, inbound := range snapshot.native.WireGuardInbounds {
		if inbound.Tag != cfg.InboundTag {
			continue
		}
		for _, peer := range inbound.Peers {
			key := peer.PublicKey
			policy, ok := cfg.Policies[key]
			through, refreshed := snapshot.native.WireGuardAccountingRefreshed[peer.UserID]
			if !ok || cfg.Peers[key] != peer.UserID ||
				!refreshed {
				continue
			}
			if !copied {
				cfg.Policies = maps.Clone(cfg.Policies)
				copied = true
			}
			policy.UsedTraffic = peer.UsedTraffic
			policy.ReflectedUsageBatchID = peer.ReflectedUsageBatchID
			policy.wireGuardReflectionThrough = through
			cfg.Policies[key] = policy
		}
	}
	return cfg
}
