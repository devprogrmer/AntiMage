package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// xrayUsageSample represents a single user's usage sample
type xrayUsageSample struct {
	UserID     int64
	InboundTag string
	Value      uint64
	Online     bool
}

// xrayUsagePendingBatch represents a pending batch awaiting ACK
type xrayUsagePendingBatch struct {
	BatchID      string
	Samples      []xrayUsageSample
	NextBaseline map[string]uint64
}

// xrayUsageDiskState is the persisted state on disk
type xrayUsageDiskState struct {
	Baseline map[string]uint64      `json:"baseline,omitempty"`
	Pending  *xrayUsagePendingBatch `json:"pending,omitempty"`
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

	raw, err := os.ReadFile(path)
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

	s.xrayUsageBaseline = state.Baseline
	s.xrayUsagePending = state.Pending
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
		Baseline: s.xrayUsageBaseline,
		Pending:  s.xrayUsagePending,
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf(
			"marshal xray usage state: %w",
			err,
		)
	}

	path := s.xrayUsageStatePath()
	tmp := path + ".tmp"

	file, err := os.OpenFile(
		tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return fmt.Errorf(
			"open temporary usage state: %w",
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
			"write temporary usage state: %w",
			err,
		)
	}

	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf(
			"sync temporary usage state: %w",
			err,
		)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"close temporary usage state: %w",
			err,
		)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"replace xray usage state: %w",
			err,
		)
	}

	return nil
}
