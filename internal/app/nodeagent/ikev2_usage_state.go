package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ikev2UsageDiskState struct {
	Baseline         map[string]uint64       `json:"baseline,omitempty"`
	Pending          *ikev2UsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                  `json:"last_acked_batch_id,omitempty"`
}

func (s *Server) ikev2UsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"ikev2",
		"usage-state.json",
	)
}

func (s *Server) ensureIKEv2UsageStateLoadedLocked() error {
	if s.ikev2UsageLoaded {
		return nil
	}

	raw, err := readOfflineAccountingState(s.ikev2UsageStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			if s.ikev2UsageBaseline == nil {
				s.ikev2UsageBaseline = make(map[string]uint64)
			}
			s.ikev2UsageLoaded = true
			return nil
		}
		return fmt.Errorf(
			"read ikev2 usage state: %w",
			err,
		)
	}

	var state ikev2UsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf(
			"parse ikev2 usage state: %w",
			err,
		)
	}

	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}
	if len(state.Baseline) > maxAccountingCounterSeries {
		return fmt.Errorf("IKEv2 usage state exceeds safe series capacity")
	}

	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"ikev2 usage state contains pending batch without id",
		)
	}

	s.ikev2UsageBaseline = state.Baseline
	s.ikev2UsagePending = state.Pending
	s.ikev2UsageLastAckedBatchID = state.LastAckedBatchID
	s.ikev2UsageLoaded = true

	return nil
}

func (s *Server) persistIKEv2UsageStateLocked() error {
	dir := filepath.Dir(s.ikev2UsageStatePath())

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create ikev2 usage state directory: %w",
			err,
		)
	}

	raw, err := json.Marshal(
		ikev2UsageDiskState{
			Baseline:         s.ikev2UsageBaseline,
			Pending:          s.ikev2UsagePending,
			LastAckedBatchID: s.ikev2UsageLastAckedBatchID,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"marshal ikev2 usage state: %w",
			err,
		)
	}

	return persistOfflineAccountingFile(s.ikev2UsageStatePath(), raw)
}
