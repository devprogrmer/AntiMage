package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type wireGuardUsageCarry struct {
	UserID       int64  `json:"user_id"`
	InboundTag   string `json:"inbound_tag"`
	Value        uint64 `json:"value"`
	NextBaseline uint64 `json:"next_baseline"`
}

type wireGuardUsageAwaitingReflectionBatch struct {
	BatchID string                 `json:"batch_id"`
	Samples []wireGuardUsageSample `json:"samples"`
}

type wireGuardUsageDiskState struct {
	Generations        map[string]wireGuardOfflineGeneration   `json:"generations,omitempty"`
	Baseline           map[string]uint64                       `json:"baseline,omitempty"`
	Pending            *wireGuardUsagePendingBatch             `json:"pending,omitempty"`
	Carry              map[string]wireGuardUsageCarry          `json:"carry,omitempty"`
	AwaitingReflection []wireGuardUsageAwaitingReflectionBatch `json:"awaiting_reflection,omitempty"`
	LastAckedBatchID   string                                  `json:"last_acked_batch_id,omitempty"`
}

func (s *Server) wireGuardUsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"wireguard",
		"usage-state.json",
	)
}

func (s *Server) ensureWireGuardUsageStateLoadedLocked() error {
	if s.wireGuardUsageLoaded {
		return nil
	}

	raw, err := readOfflineAccountingState(s.wireGuardUsageStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			if s.wireGuardUsageBaseline == nil {
				s.wireGuardUsageBaseline = make(map[string]uint64)
			}
			if s.wireGuardUsageCarry == nil {
				s.wireGuardUsageCarry = make(map[string]wireGuardUsageCarry)
			}
			s.wireGuardUsageLoaded = true
			return nil
		}
		return fmt.Errorf("read wireguard usage state: %w", err)
	}

	var state wireGuardUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse wireguard usage state: %w", err)
	}
	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}
	if state.Carry == nil {
		state.Carry = make(map[string]wireGuardUsageCarry)
	}

	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"wireguard usage state contains pending batch without id",
		)
	}

	s.wireGuardUsageBaseline = state.Baseline
	s.wireGuardUsagePending = state.Pending
	s.wireGuardUsageCarry = state.Carry
	s.wireGuardUsageAwaitingReflection = state.AwaitingReflection
	s.wireGuardUsageLastAckedBatchID = state.LastAckedBatchID
	s.wireGuardUsageLoaded = true
	return nil
}

func (s *Server) persistWireGuardUsageStateLocked() error {
	state, err := s.readWireGuardGenerationStateLocked()
	if err != nil {
		return err
	}
	return s.persistWireGuardGenerationStateLocked(state)
}

func (s *Server) persistWireGuardGenerationStateLocked(generations map[string]wireGuardOfflineGeneration) error {
	for key, generation := range generations {
		if value, ok := s.wireGuardUsageBaseline[key]; ok {
			generation.Reference = value
		}
		if pending := s.wireGuardUsagePending; pending != nil {
			if value, ok := pending.NextBaseline[key]; ok {
				generation.Reference = value
			}
		}
		if carry, ok := s.wireGuardUsageCarry[key]; ok {
			generation.Reference = carry.NextBaseline
		}
		generations[key] = generation
	}
	dir := filepath.Dir(s.wireGuardUsageStatePath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create wireguard usage state directory: %w",
			err,
		)
	}

	raw, err := json.Marshal(wireGuardUsageDiskState{
		Generations:        generations,
		Baseline:           s.wireGuardUsageBaseline,
		Pending:            s.wireGuardUsagePending,
		Carry:              s.wireGuardUsageCarry,
		AwaitingReflection: s.wireGuardUsageAwaitingReflection,
		LastAckedBatchID:   s.wireGuardUsageLastAckedBatchID,
	})
	if err != nil {
		return fmt.Errorf("marshal wireguard usage state: %w", err)
	}

	path := s.wireGuardUsageStatePath()
	return wireGuardAccountingWrite(path, raw)
}
