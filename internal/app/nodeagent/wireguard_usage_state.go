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
	JournalSequence    uint64                                  `json:"journal_sequence,omitempty"`
	Generations        map[string]wireGuardOfflineGeneration   `json:"generations,omitempty"`
	Baseline           map[string]uint64                       `json:"baseline,omitempty"`
	Pending            *wireGuardUsagePendingBatch             `json:"pending,omitempty"`
	Carry              map[string]wireGuardUsageCarry          `json:"carry,omitempty"`
	AwaitingReflection []wireGuardUsageAwaitingReflectionBatch `json:"awaiting_reflection,omitempty"`
	LastAckedBatchID   string                                  `json:"last_acked_batch_id,omitempty"`
}

type wireGuardAccountingJournalRecord struct {
	Sequence          uint64                                `json:"sequence"`
	Generations       map[string]wireGuardOfflineGeneration `json:"generations,omitempty"`
	RemovedGeneration []string                              `json:"removed_generations,omitempty"`
	Carry             map[string]wireGuardUsageCarry        `json:"carry,omitempty"`
	RemovedCarry      []string                              `json:"removed_carry,omitempty"`
}

const (
	wireGuardJournalCompactRecords = 128
	wireGuardJournalCompactBytes   = 256 << 10
)

var wireGuardAccountingJournalAppend = appendAccountingWAL
var wireGuardAccountingJournalCompact = compactAccountingWAL

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
			if s.wireGuardUsageGenerations == nil {
				s.wireGuardUsageGenerations = make(map[string]wireGuardOfflineGeneration)
			}
			s.wireGuardGenerationsLoaded = true
			s.wireGuardSnapshotPresent = false
			if err := s.replayWireGuardAccountingJournalLocked(); err != nil {
				return fmt.Errorf("replay wireguard accounting journal: %w", err)
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
	s.wireGuardUsageGenerations = state.Generations
	if s.wireGuardUsageGenerations == nil {
		s.wireGuardUsageGenerations = make(map[string]wireGuardOfflineGeneration)
	}
	s.wireGuardGenerationsLoaded = true
	s.wireGuardSnapshotPresent = true
	s.wireGuardSnapshotSequence = state.JournalSequence
	s.wireGuardJournalSequence = state.JournalSequence
	if err := s.replayWireGuardAccountingJournalLocked(); err != nil {
		return fmt.Errorf("replay wireguard accounting journal: %w", err)
	}
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
	// Callers receive a working copy from readWireGuardGenerationStateLocked;
	// keep the committed cache untouched until the replacement is durable.
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
		JournalSequence:    s.wireGuardJournalSequence,
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
	if err := wireGuardAccountingWrite(path, raw); err != nil {
		return err
	}
	s.wireGuardUsageGenerations = generations
	s.wireGuardGenerationsLoaded = true
	s.wireGuardSnapshotPresent = true
	s.wireGuardSnapshotSequence = s.wireGuardJournalSequence
	if err := wireGuardAccountingJournalCompact(s.wireGuardUsageJournalPath()); err != nil {
		info, statErr := os.Stat(s.wireGuardUsageJournalPath())
		if os.IsNotExist(statErr) || (statErr == nil && info.Size() == 0) {
			s.wireGuardJournalOffset = 0
			s.wireGuardJournalRecords = 0
		}
		s.appendLog("wireguard accounting journal compaction deferred: " + err.Error())
		return nil // The snapshot is already durable and includes this sequence.
	}
	s.wireGuardJournalOffset = 0
	s.wireGuardJournalRecords = 0
	return nil
}

func (s *Server) wireGuardUsageJournalPath() string {
	return s.wireGuardUsageStatePath() + ".wal"
}

func (s *Server) replayWireGuardAccountingJournalLocked() error {
	sequence := s.wireGuardSnapshotSequence
	offset, records, err := replayAccountingWAL(s.wireGuardUsageJournalPath(), func(raw []byte) error {
		var record wireGuardAccountingJournalRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("decode wireguard accounting journal record: %w", err)
		}
		if record.Sequence == 0 {
			return fmt.Errorf("wireguard accounting journal has zero sequence")
		}
		if record.Sequence <= s.wireGuardSnapshotSequence {
			return nil
		}
		if record.Sequence != sequence+1 {
			return fmt.Errorf("wireguard accounting journal sequence %d follows %d", record.Sequence, sequence)
		}
		for _, key := range record.RemovedGeneration {
			delete(s.wireGuardUsageGenerations, key)
		}
		for key, generation := range record.Generations {
			s.wireGuardUsageGenerations[key] = generation
		}
		for _, key := range record.RemovedCarry {
			delete(s.wireGuardUsageCarry, key)
		}
		for key, carry := range record.Carry {
			s.wireGuardUsageCarry[key] = carry
		}
		if len(s.wireGuardUsageGenerations) > maxAccountingCounterSeries || len(s.wireGuardUsageCarry) > maxAccountingCounterSeries {
			return fmt.Errorf("replayed wireguard accounting exceeds capacity")
		}
		sequence = record.Sequence
		return nil
	})
	if err != nil {
		return err
	}
	if !s.wireGuardSnapshotPresent && records > 0 {
		return fmt.Errorf("wireguard accounting journal exists without a base snapshot")
	}
	s.wireGuardJournalSequence = sequence
	s.wireGuardJournalOffset = offset
	s.wireGuardJournalRecords = records
	return nil
}
