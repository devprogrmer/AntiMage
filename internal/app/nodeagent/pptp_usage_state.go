package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pptpUsageDiskState struct {
	Baseline         map[string]uint64      `json:"baseline,omitempty"`
	Pending          *pptpUsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                 `json:"last_acked_batch_id,omitempty"`
}

func (s *Server) pptpUsageStatePath() string {
	return filepath.Join(s.cfg.DataDir, "pptp", "usage-state.json")
}

func (s *Server) ensurePPTPUsageStateLoadedLocked() error {
	if s.pptpUsageLoaded {
		return nil
	}

	raw, err := os.ReadFile(s.pptpUsageStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			if s.pptpUsageBaseline == nil {
				s.pptpUsageBaseline = make(map[string]uint64)
			}
			s.pptpUsageLoaded = true
			return nil
		}
		return fmt.Errorf("read pptp usage state: %w", err)
	}

	var state pptpUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse pptp usage state: %w", err)
	}
	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}
	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"pptp usage state contains pending batch without id",
		)
	}

	s.pptpUsageBaseline = state.Baseline
	s.pptpUsagePending = state.Pending
	s.pptpUsageLastAckedBatchID = state.LastAckedBatchID
	s.pptpUsageLoaded = true
	return nil
}

func (s *Server) persistPPTPUsageStateLocked() error {
	dir := filepath.Dir(s.pptpUsageStatePath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create pptp usage state directory: %w",
			err,
		)
	}

	raw, err := json.Marshal(pptpUsageDiskState{
		Baseline:         s.pptpUsageBaseline,
		Pending:          s.pptpUsagePending,
		LastAckedBatchID: s.pptpUsageLastAckedBatchID,
	})
	if err != nil {
		return fmt.Errorf("marshal pptp usage state: %w", err)
	}

	path := s.pptpUsageStatePath()
	tmp := path + ".tmp"
	file, err := os.OpenFile(
		tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return fmt.Errorf(
			"open temporary pptp usage state: %w",
			err,
		)
	}

	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}
	if _, err := file.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf(
			"write temporary pptp usage state: %w",
			err,
		)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf(
			"sync temporary pptp usage state: %w",
			err,
		)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"close temporary pptp usage state: %w",
			err,
		)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"replace pptp usage state: %w",
			err,
		)
	}
	return nil
}
