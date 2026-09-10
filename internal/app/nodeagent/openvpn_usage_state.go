package nodeagent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type openVPNUsageDiskState struct {
	Baseline map[string]uint64         `json:"baseline,omitempty"`
	Pending  *openVPNUsagePendingBatch `json:"pending,omitempty"`
}

func (s *Server) openVPNUsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"openvpn",
		"usage-state.json",
	)
}

func (s *Server) ensureOpenVPNUsageStateLoadedLocked() error {
	if s.openVPNUsageLoaded {
		return nil
	}

	path := s.openVPNUsageStatePath()

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.openVPNUsageBaseline == nil {
				s.openVPNUsageBaseline = make(map[string]uint64)
			}
			s.openVPNUsageLoaded = true
			return nil
		}

		return fmt.Errorf(
			"read openvpn usage state: %w",
			err,
		)
	}

	var state openVPNUsageDiskState

	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf(
			"parse openvpn usage state: %w",
			err,
		)
	}

	if state.Baseline == nil {
		state.Baseline = make(map[string]uint64)
	}

	if state.Pending != nil &&
		strings.TrimSpace(state.Pending.BatchID) == "" {
		return fmt.Errorf(
			"openvpn usage state contains pending batch without id",
		)
	}

	s.openVPNUsageBaseline = state.Baseline
	s.openVPNUsagePending = state.Pending
	s.openVPNUsageLoaded = true

	return nil
}

func (s *Server) persistOpenVPNUsageStateLocked() error {
	dir := filepath.Dir(s.openVPNUsageStatePath())

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create openvpn usage state directory: %w",
			err,
		)
	}

	state := openVPNUsageDiskState{
		Baseline: s.openVPNUsageBaseline,
		Pending:  s.openVPNUsagePending,
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf(
			"marshal openvpn usage state: %w",
			err,
		)
	}

	path := s.openVPNUsageStatePath()
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
			"replace openvpn usage state: %w",
			err,
		)
	}

	return nil
}

func readAllOpenVPNUsageState(
	reader io.Reader,
) (openVPNUsageDiskState, error) {
	raw, err := io.ReadAll(reader)
	if err != nil {
		return openVPNUsageDiskState{}, err
	}

	var state openVPNUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return openVPNUsageDiskState{}, err
	}

	return state, nil
}
