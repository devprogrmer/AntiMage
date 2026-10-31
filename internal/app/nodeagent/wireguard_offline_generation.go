package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Generation survives ACK pruning because it belongs to the counter, not carry.
type wireGuardOfflineGeneration struct {
	Identity      string `json:"identity"`
	Absent        bool   `json:"absent,omitempty"`
	UserID        int64  `json:"user_id"`
	Total         uint64 `json:"total"`
	Reference     uint64 `json:"reference"`
	Transitioning bool   `json:"transitioning,omitempty"`
}

var wireGuardInterfaceIdentity = func(name string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", nil
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	index, err := os.ReadFile(filepath.Join("/sys/class/net", name, "ifindex"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(boot)) + ":" + strings.TrimSpace(string(index)), nil
}

var wireGuardAccountingWrite = writeAccountingState

func (s *Server) readWireGuardGenerationStateLocked() (map[string]wireGuardOfflineGeneration, error) {
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.wireGuardGenerationsLoaded {
		return cloneWireGuardGenerations(s.wireGuardUsageGenerations), nil
	}
	raw, err := readOfflineAccountingState(s.wireGuardUsageStatePath())
	if os.IsNotExist(err) {
		s.wireGuardUsageGenerations = make(map[string]wireGuardOfflineGeneration)
		s.wireGuardGenerationsLoaded = true
		return cloneWireGuardGenerations(s.wireGuardUsageGenerations), nil
	}
	if err != nil {
		return nil, err
	}
	var state wireGuardUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Generations == nil {
		state.Generations = map[string]wireGuardOfflineGeneration{}
	}
	s.wireGuardUsageGenerations = state.Generations
	s.wireGuardGenerationsLoaded = true
	return cloneWireGuardGenerations(s.wireGuardUsageGenerations), nil
}

func cloneWireGuardGenerations(source map[string]wireGuardOfflineGeneration) map[string]wireGuardOfflineGeneration {
	clone := make(map[string]wireGuardOfflineGeneration, len(source))
	for key, generation := range source {
		clone[key] = generation
	}
	return clone
}

// Caller holds usageMu. Carry and generation are committed in the same durable write.
func (s *Server) checkpointWireGuardGenerationLocked(cfg wireGuardUsageRuntimeConfig, name string, peers []wireGuardPeerCounters, capture bool) error {
	if !wireGuardUsageAccountingEnabled(cfg) {
		return nil
	}
	generations, err := s.readWireGuardGenerationStateLocked()
	if err != nil {
		return err
	}
	identity, err := wireGuardInterfaceIdentity(name)
	if err != nil {
		return fmt.Errorf("wireguard generation identity: %w", err)
	}
	type carryUndo struct {
		value  wireGuardUsageCarry
		exists bool
	}
	var carryUndoByKey map[string]carryUndo
	rememberCarry := func(key string, previous wireGuardUsageCarry, existed bool) {
		if carryUndoByKey == nil {
			carryUndoByKey = make(map[string]carryUndo)
		}
		if _, recorded := carryUndoByKey[key]; !recorded {
			carryUndoByKey[key] = carryUndo{value: previous, exists: existed}
		}
	}
	restoreCarry := func() {
		for key, previous := range carryUndoByKey {
			if previous.exists {
				s.wireGuardUsageCarry[key] = previous.value
			} else {
				delete(s.wireGuardUsageCarry, key)
			}
		}
	}
	changed := false
	seen := map[string]bool{}
	for _, peer := range peers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		uid := cfg.Peers[publicKey]
		if uid <= 0 {
			continue
		}
		key := wireGuardUsageBaselineKey(cfg.InboundTag, name, publicKey)
		seen[key] = true
		old, exists := generations[key]
		if old.Transitioning {
			restoreCarry()
			return fmt.Errorf("wireguard interrupted peer generation transition")
		}
		if exists && old.UserID != uid {
			restoreCarry()
			return fmt.Errorf("wireguard generation ownership changed")
		}
		if exists && (old.Absent || old.Identity != identity) {
			carry := s.wireGuardUsageCarry[key]
			reference := old.Reference
			if v, ok := s.wireGuardUsageBaseline[key]; ok {
				reference = v
			}
			if p := s.wireGuardUsagePending; p != nil {
				if v, ok := p.NextBaseline[key]; ok {
					reference = v
				}
			}
			if v, ok := s.wireGuardUsageCarry[key]; ok {
				reference = v.NextBaseline
			}
			// The carry may already have advanced beyond the last persisted
			// generation total (for example, removal starts after a final
			// collection but before the next generation checkpoint). In that
			// case the old snapshot contributes no additional bytes; treating
			// it as a counter reset would rebill the entire prior generation.
			delta := uint64(0)
			if old.Total > reference {
				delta = old.Total - reference
			}
			if ^uint64(0)-carry.Value < delta {
				restoreCarry()
				return fmt.Errorf("wireguard generation carry overflow")
			}
			carry.Value += delta
			carry.UserID, carry.InboundTag, carry.NextBaseline = uid, cfg.InboundTag, 0
			previousCarry, carryExists := s.wireGuardUsageCarry[key]
			if !carryExists || previousCarry != carry {
				rememberCarry(key, previousCarry, carryExists)
				if s.wireGuardUsageCarry == nil {
					s.wireGuardUsageCarry = map[string]wireGuardUsageCarry{}
				}
				s.wireGuardUsageCarry[key] = carry
				changed = true
			}
		}
		total, err := wireGuardPeerTotalBytes(peer)
		if err == nil && (capture || (exists && (old.Absent || old.Identity != identity))) {
			var carryChanged bool
			carryChanged, err = s.updateWireGuardUsageCarryLockedWithChange(cfg.InboundTag, name, publicKey, uid, total, false, rememberCarry)
			changed = changed || carryChanged
		}
		if err != nil {
			restoreCarry()
			return err
		}
		nextGeneration := old
		if capture || !exists || old.Absent || old.Identity != identity {
			nextGeneration = wireGuardOfflineGeneration{Identity: identity, UserID: uid, Total: total, Reference: old.Reference}
		}
		if reference, ok := s.wireGuardUsageBaseline[key]; ok {
			nextGeneration.Reference = reference
		}
		if pending := s.wireGuardUsagePending; pending != nil {
			if reference, ok := pending.NextBaseline[key]; ok {
				nextGeneration.Reference = reference
			}
		}
		if carry, ok := s.wireGuardUsageCarry[key]; ok {
			nextGeneration.Reference = carry.NextBaseline
		}
		if !exists || nextGeneration != old {
			generations[key] = nextGeneration
			changed = true
		}
	}
	prefix := wireGuardUsageBaselineKey(cfg.InboundTag, name, "")
	for key, generation := range generations {
		if strings.HasPrefix(key, prefix) && !seen[key] {
			if !generation.Absent {
				changed = true
				generation.Absent = true
				generations[key] = generation
			}
		}
	}
	if len(generations) > maxAccountingCounterSeries {
		restoreCarry()
		return fmt.Errorf("wireguard generation capacity exceeded")
	}
	if !changed {
		return nil
	}
	if !s.wireGuardSnapshotPresent {
		if err := s.persistWireGuardGenerationStateLocked(generations); err != nil {
			restoreCarry()
			return err
		}
		return nil
	}
	record := wireGuardAccountingJournalRecord{Sequence: s.wireGuardJournalSequence + 1}
	for key, generation := range generations {
		if previous, exists := s.wireGuardUsageGenerations[key]; !exists || previous != generation {
			if record.Generations == nil {
				record.Generations = make(map[string]wireGuardOfflineGeneration, 4)
			}
			record.Generations[key] = generation
		}
	}
	for key := range s.wireGuardUsageGenerations {
		if _, exists := generations[key]; !exists {
			record.RemovedGeneration = append(record.RemovedGeneration, key)
		}
	}
	for key := range carryUndoByKey {
		if carry, exists := s.wireGuardUsageCarry[key]; exists {
			if record.Carry == nil {
				record.Carry = make(map[string]wireGuardUsageCarry, len(carryUndoByKey))
			}
			record.Carry[key] = carry
		} else {
			record.RemovedCarry = append(record.RemovedCarry, key)
		}
	}
	journalRaw, err := json.Marshal(record)
	if err != nil {
		restoreCarry()
		return fmt.Errorf("marshal wireguard accounting journal: %w", err)
	}
	newOffset, err := wireGuardAccountingJournalAppend(s.wireGuardUsageJournalPath(), s.wireGuardJournalOffset, journalRaw)
	if err != nil {
		restoreCarry()
		return err
	}
	s.wireGuardUsageGenerations = generations
	s.wireGuardJournalSequence = record.Sequence
	s.wireGuardJournalOffset = newOffset
	s.wireGuardJournalRecords++
	if s.wireGuardJournalRecords >= wireGuardJournalCompactRecords || s.wireGuardJournalOffset >= wireGuardJournalCompactBytes {
		if err := s.persistWireGuardGenerationStateLocked(generations); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) checkpointWireGuardOfflineGeneration(ctx context.Context, interfaces ...string) error {
	return s.checkWireGuardOfflineGeneration(ctx, false, interfaces...)
}

func (s *Server) quotaCheckWireGuardOfflineGeneration(ctx context.Context) error {
	return s.checkWireGuardOfflineGeneration(ctx, true)
}

func (s *Server) checkWireGuardOfflineGeneration(ctx context.Context, enforce bool, interfaces ...string) error {
	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return err
	}
	root := filepath.Join(s.cfg.DataDir, "wireguard", "inbounds")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "usage-helper.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var cfg wireGuardUsageRuntimeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		// Legacy port-only helpers are resolved by the normal collection path.
		if cfg.InterfaceName == "" {
			continue
		}
		if len(interfaces) > 0 && cfg.InterfaceName != interfaces[0] {
			continue
		}
		raw, err = wireGuardDumpInterface(ctx, cfg.InterfaceName)
		if err != nil {
			return err
		}
		peers, err := parseWireGuardDump(string(raw))
		if err != nil {
			return err
		}
		if err := s.checkpointWireGuardGenerationLocked(cfg, cfg.InterfaceName, peers, true); err != nil {
			return err
		}
		if enforce {
			s.enforceWireGuardPoliciesLocked(cfg, cfg.InterfaceName, peers, time.Now().UTC())
		}
	}
	return nil
}

// The parent calls this before peer removal and again after successful removal.
// Failed/interrupted removals leave a transition requiring reconciliation.
func (s *Server) transitionWireGuardPeerGenerationLocked(tag, name, publicKey string, before bool) error {
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return err
	}
	generations, err := s.readWireGuardGenerationStateLocked()
	if err != nil {
		return err
	}
	key := wireGuardUsageBaselineKey(tag, name, publicKey)
	generation, exists := generations[key]
	if !exists {
		return nil
	}
	generation.Transitioning = before
	if !before {
		generation.Absent = true
	}
	generations[key] = generation
	return s.persistWireGuardGenerationStateLocked(generations)
}

func (s *Server) markWireGuardRemovedGenerations(prepared preparedWireGuardRuntime) error {
	return s.transitionWireGuardRemovedGenerations(prepared, false)
}

func (s *Server) transitionWireGuardRemovedGenerations(prepared preparedWireGuardRuntime, before bool) error {
	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()
	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return err
	}
	generations, err := s.readWireGuardGenerationStateLocked()
	if err != nil {
		return err
	}
	retained := map[string]bool{}
	for _, peer := range prepared.Inbound.Peers {
		retained[strings.TrimSpace(peer.PublicKey)] = true
	}
	for _, peer := range prepared.SuppressedPeers {
		delete(retained, strings.TrimSpace(peer.PublicKey))
	}
	prefix := wireGuardUsageBaselineKey(prepared.Tag, prepared.InterfaceName, "")
	for key, generation := range generations {
		if strings.HasPrefix(key, prefix) && !retained[strings.TrimPrefix(key, prefix)] {
			generation.Transitioning = before
			if !before {
				generation.Absent = true
			}
			generations[key] = generation
		}
	}
	return s.persistWireGuardGenerationStateLocked(generations)
}
