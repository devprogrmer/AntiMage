package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var xrayAccountingWrite = writeAccountingState
var xrayAccountingJournalAppend = appendAccountingWAL
var xrayAccountingJournalCompact = compactAccountingWAL

const xrayAccountingJournalCompactRecords = 128
const xrayAccountingJournalCompactBytes = 256 << 10

type xrayAccountingJournalEntry struct {
	Key     string            `json:"key"`
	Counter accountingCounter `json:"counter"`
}

type xrayAccountingJournalRecord struct {
	Sequence   uint64                       `json:"sequence"`
	Generation string                       `json:"generation,omitempty"`
	Full       bool                         `json:"full,omitempty"`
	Entries    []xrayAccountingJournalEntry `json:"entries,omitempty"`
	Removed    []string                     `json:"removed,omitempty"`
}

// xrayUsageSample represents a single user's usage sample
type xrayUsageSample struct {
	UserID     int64
	InboundTag string
	Value      uint64
	Upload     uint64
	Download   uint64
	Online     bool
}

type xrayOnlineIPSnapshot struct {
	IP           string
	LastSeenUnix int64
}

type xrayOnlineUserSnapshot struct {
	UserID int64
	Email  string
	IPs    []xrayOnlineIPSnapshot
}

// xrayUsagePendingBatch represents a pending batch awaiting ACK.
type xrayUsagePendingBatch struct {
	BatchID          string
	Samples          []xrayUsageSample
	OnlineUsers      []xrayOnlineUserSnapshot
	NextBaseline     map[string]uint64
	IntervalSeconds  float64
	NextBaselineAt   time.Time
	SpeedUnitVersion int
}

// xrayUsageDiskState is the persisted state on disk
type xrayUsageDiskState struct {
	Generation       string                       `json:"generation,omitempty"`
	JournalSequence  uint64                       `json:"journal_sequence,omitempty"`
	Counters         map[string]accountingCounter `json:"counters,omitempty"`
	Baseline         map[string]uint64            `json:"baseline,omitempty"`
	Pending          *xrayUsagePendingBatch       `json:"pending,omitempty"`
	LastAckedBatchID string                       `json:"last_acked_batch_id,omitempty"`
	BaselineAt       time.Time                    `json:"baseline_at,omitempty"`
}

func (s *Server) xrayUsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"xray",
		"usage-state.json",
	)
}

func (s *Server) xrayUsageJournalPath() string {
	return s.xrayUsageStatePath() + ".wal"
}

func (s *Server) ensureXrayUsageStateLoadedLocked() error {
	if s.xrayUsageLoaded {
		return nil
	}

	path := s.xrayUsageStatePath()

	raw, err := readOfflineAccountingState(path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.xrayUsageBaseline == nil {
				s.xrayUsageBaseline = make(map[string]uint64)
			}
			if s.xrayAccountingCounters == nil {
				s.xrayAccountingCounters = make(map[string]accountingCounter)
			}
		} else {
			return fmt.Errorf("read xray usage state: %w", err)
		}
	} else {
		var state xrayUsageDiskState
		if err := json.Unmarshal(raw, &state); err != nil {
			return fmt.Errorf("parse xray usage state: %w", err)
		}
		if state.Baseline == nil {
			state.Baseline = make(map[string]uint64)
		}
		if state.Counters == nil {
			state.Counters = make(map[string]accountingCounter)
		}
		if state.Pending != nil && strings.TrimSpace(state.Pending.BatchID) == "" {
			return fmt.Errorf("xray usage state contains pending batch without id")
		}
		s.xrayAccountingSnapshotPresent = true
		s.xrayUsageBaseline = state.Baseline
		s.xrayAccountingCounters = state.Counters
		s.xrayAccountingGeneration = state.Generation
		s.xrayAccountingSnapshotSequence = state.JournalSequence
		s.xrayAccountingJournalSequence = state.JournalSequence
		s.xrayUsagePending = state.Pending
		s.xrayUsageLastAckedBatchID = state.LastAckedBatchID
		s.xrayUsageBaselineAt = state.BaselineAt
	}
	if err := s.replayXrayAccountingJournalLocked(); err != nil {
		return fmt.Errorf("replay xray accounting journal: %w", err)
	}
	if len(s.xrayAccountingCounters) > maxAccountingCounterSeries {
		return fmt.Errorf("persisted xray accounting exceeds series capacity")
	}
	for key, counter := range s.xrayAccountingCounters {
		if err := validateXrayAccountingCounter(key, counter); err != nil {
			return err
		}
	}
	s.xrayUsageLoaded = true

	return nil
}

func validateXrayAccountingCounter(key string, counter accountingCounter) error {
	separator := strings.LastIndexByte(key, ':')
	if separator < 0 || (key[separator+1:] != "uplink" && key[separator+1:] != "downlink") {
		return fmt.Errorf("invalid persisted xray accounting key %q", key)
	}
	identity, err := parseXrayUserEmail(key[:separator])
	if err != nil || identity.UserID <= 0 || counter.Native > counter.Total {
		return fmt.Errorf("invalid persisted xray accounting counter %q", key)
	}
	return nil
}

func (s *Server) replayXrayAccountingJournalLocked() error {
	sequence := s.xrayAccountingSnapshotSequence
	offset, records, err := replayAccountingWAL(s.xrayUsageJournalPath(), func(raw []byte) error {
		var record xrayAccountingJournalRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("decode xray accounting journal record: %w", err)
		}
		if record.Sequence == 0 {
			return fmt.Errorf("xray accounting journal has zero sequence")
		}
		if record.Sequence <= s.xrayAccountingSnapshotSequence {
			return nil
		}
		if record.Sequence != sequence+1 {
			return fmt.Errorf("xray accounting journal sequence %d follows %d", record.Sequence, sequence)
		}
		if record.Full {
			s.xrayAccountingCounters = make(map[string]accountingCounter, len(record.Entries))
			s.xrayAccountingGeneration = record.Generation
		} else if record.Generation != s.xrayAccountingGeneration {
			return fmt.Errorf("xray accounting journal generation changed without full record")
		}
		for _, key := range record.Removed {
			delete(s.xrayAccountingCounters, key)
		}
		for _, entry := range record.Entries {
			s.xrayAccountingCounters[entry.Key] = entry.Counter
		}
		if len(s.xrayAccountingCounters) > maxAccountingCounterSeries {
			return fmt.Errorf("replayed xray accounting exceeds series capacity")
		}
		sequence = record.Sequence
		return nil
	})
	if err != nil {
		return err
	}
	s.xrayAccountingJournalSequence = sequence
	s.xrayAccountingJournalOffset = offset
	s.xrayAccountingJournalRecords = records
	return nil
}

func (s *Server) persistXrayUsageStateLocked() error {
	dir := filepath.Dir(s.xrayUsageStatePath())

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create xray usage state directory: %w",
			err,
		)
	}

	state := xrayUsageDiskState{
		Generation:       s.xrayAccountingGeneration,
		JournalSequence:  s.xrayAccountingJournalSequence,
		Counters:         s.xrayAccountingCounters,
		Baseline:         s.xrayUsageBaseline,
		Pending:          s.xrayUsagePending,
		LastAckedBatchID: s.xrayUsageLastAckedBatchID,
		BaselineAt:       s.xrayUsageBaselineAt,
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf(
			"marshal xray usage state: %w",
			err,
		)
	}

	if err := xrayAccountingWrite(s.xrayUsageStatePath(), raw); err != nil {
		return err
	}
	s.xrayAccountingSnapshotSequence = s.xrayAccountingJournalSequence
	s.xrayAccountingSnapshotPresent = true
	if err := xrayAccountingJournalCompact(s.xrayUsageJournalPath()); err != nil {
		info, statErr := os.Stat(s.xrayUsageJournalPath())
		if os.IsNotExist(statErr) || (statErr == nil && info.Size() == 0) {
			s.xrayAccountingJournalOffset = 0
			s.xrayAccountingJournalRecords = 0
		}
		s.appendLog("xray accounting journal compaction deferred: " + err.Error())
		return nil // The snapshot is already durable and includes this sequence.
	}
	s.xrayAccountingJournalOffset = 0
	s.xrayAccountingJournalRecords = 0
	return nil
}
