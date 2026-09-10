package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// xrayOutboundUsageSample represents outbound traffic for one tag
type xrayOutboundUsageSample struct {
	Tag  string
	Up   uint64
	Down uint64
}

// xrayOutboundUsagePendingBatch represents a pending outbound batch awaiting ACK
type xrayOutboundUsagePendingBatch struct {
	BatchID      string
	Samples      []xrayOutboundUsageSample
	NextBaseline map[string]uint64
}

// xrayOutboundUsageDiskState is the persisted outbound state on disk
type xrayOutboundUsageDiskState struct {
	Baseline map[string]uint64              `json:"baseline,omitempty"`
	Pending  *xrayOutboundUsagePendingBatch `json:"pending,omitempty"`
}

func (s *Server) xrayOutboundUsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"xray",
		"outbound-usage-state.json",
	)
}

func (s *Server) ensureXrayOutboundUsageStateLoadedLocked() error {
	if s.xrayOutboundUsageLoaded {
		return nil
	}

	path := s.xrayOutboundUsageStatePath()

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.xrayOutboundUsageBaseline == nil {
				s.xrayOutboundUsageBaseline = make(map[string]uint64)
			}
			s.xrayOutboundUsageLoaded = true
			return nil
		}

		return fmt.Errorf(
			"read xray outbound usage state: %w",
			err,
		)
	}

	var state xrayOutboundUsageDiskState

	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf(
			"parse xray outbound usage state: %w",
			err,
		)
	}

	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}

	s.xrayOutboundUsageBaseline = state.Baseline
	s.xrayOutboundUsagePending = state.Pending
	s.xrayOutboundUsageLoaded = true

	return nil
}

func (s *Server) persistXrayOutboundUsageStateLocked() error {
	dir := filepath.Dir(s.xrayOutboundUsageStatePath())

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create xray outbound usage state directory: %w",
			err,
		)
	}

	state := xrayOutboundUsageDiskState{
		Baseline: s.xrayOutboundUsageBaseline,
		Pending:  s.xrayOutboundUsagePending,
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf(
			"marshal xray outbound usage state: %w",
			err,
		)
	}

	path := s.xrayOutboundUsageStatePath()
	tmp := path + ".tmp"

	file, err := os.OpenFile(
		tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return fmt.Errorf(
			"open temporary outbound usage state: %w",
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
			"write temporary outbound usage state: %w",
			err,
		)
	}

	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf(
			"sync temporary outbound usage state: %w",
			err,
		)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"close temporary outbound usage state: %w",
			err,
		)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"replace xray outbound usage state: %w",
			err,
		)
	}

	return nil
}
