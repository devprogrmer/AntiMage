package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type l2TPUsageDiskState struct {
	Baseline         map[string]uint64      `json:"baseline,omitempty"`
	Pending          *l2TPUsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                 `json:"last_acked_batch_id,omitempty"`
}

func (s *Server) l2TPUsageStatePath() string {
	return filepath.Join(s.cfg.DataDir, "l2tp", "usage-state.json")
}

func (s *Server) ensureL2TPUsageStateLoadedLocked() error {
	if s.l2TPUsageLoaded {
		return nil
	}

	raw, err := os.ReadFile(s.l2TPUsageStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			if s.l2TPUsageBaseline == nil {
				s.l2TPUsageBaseline = make(map[string]uint64)
			}
			s.l2TPUsageLoaded = true
			return nil
		}
		return fmt.Errorf("read l2tp usage state: %w", err)
	}

	var state l2TPUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse l2tp usage state: %w", err)
	}
	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}
	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"l2tp usage state contains pending batch without id",
		)
	}

	s.l2TPUsageBaseline = state.Baseline
	if state.Pending != nil && state.Pending.SeenUnix == 0 {
		info, err := os.Stat(s.l2TPUsageStatePath())
		if err != nil {
			return err
		}
		state.Pending.SeenUnix = info.ModTime().UTC().Unix()
	}
	s.l2TPUsagePending = state.Pending
	s.l2TPUsageLastAckedBatchID = state.LastAckedBatchID
	s.l2TPUsageLoaded = true
	return nil
}

func (s *Server) persistL2TPUsageStateLocked() error {
	dir := filepath.Dir(s.l2TPUsageStatePath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create l2tp usage state directory: %w",
			err,
		)
	}

	raw, err := json.Marshal(l2TPUsageDiskState{
		Baseline:         s.l2TPUsageBaseline,
		Pending:          s.l2TPUsagePending,
		LastAckedBatchID: s.l2TPUsageLastAckedBatchID,
	})
	if err != nil {
		return fmt.Errorf("marshal l2tp usage state: %w", err)
	}

	return writeAccountingState(s.l2TPUsageStatePath(), raw)
}
