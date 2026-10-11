package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
			s.xrayUsageLoaded = true
			return nil
		}

		return fmt.Errorf(
			"read xray usage state: %w",
			err,
		)
	}

	var state xrayUsageDiskState

	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf(
			"parse xray usage state: %w",
			err,
		)
	}

	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}

	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"xray usage state contains pending batch without id",
		)
	}
	if len(state.Counters) > maxAccountingCounterSeries {
		return fmt.Errorf("persisted xray accounting exceeds series capacity")
	}
	for key, counter := range state.Counters {
		separator := strings.LastIndexByte(key, ':')
		if separator < 0 || (key[separator+1:] != "uplink" && key[separator+1:] != "downlink") {
			return fmt.Errorf("invalid persisted xray accounting key %q", key)
		}
		identity, err := parseXrayUserEmail(key[:separator])
		if err != nil || identity.UserID <= 0 || counter.Native > counter.Total {
			return fmt.Errorf("invalid persisted xray accounting counter %q", key)
		}
	}

	s.xrayUsageBaseline = state.Baseline
	s.xrayAccountingCounters = state.Counters
	s.xrayAccountingGeneration = state.Generation
	s.xrayUsagePending = state.Pending
	s.xrayUsageLastAckedBatchID = state.LastAckedBatchID
	s.xrayUsageBaselineAt = state.BaselineAt
	s.xrayUsageLoaded = true

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

	return writeAccountingState(s.xrayUsageStatePath(), raw)
}
