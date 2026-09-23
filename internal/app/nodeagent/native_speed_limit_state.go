package nodeagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type nativeSpeedLimitState struct {
	Interfaces  []string                `json:"interfaces,omitempty"`
	Actions     []uint32                `json:"actions,omitempty"`
	Attachments []nativeSpeedAttachment `json:"attachments,omitempty"`
}

func (s *Server) nativeSpeedLimitStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"speed-limits",
		"state.json",
	)
}

func (s *Server) loadNativeSpeedLimitState() (
	nativeSpeedLimitState,
	error,
) {
	path := s.nativeSpeedLimitStatePath()

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nativeSpeedLimitState{}, nil
	}
	if err != nil {
		return nativeSpeedLimitState{}, err
	}

	var state nativeSpeedLimitState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nativeSpeedLimitState{}, err
	}

	return state, nil
}

func (s *Server) saveNativeSpeedLimitState(
	state nativeSpeedLimitState,
) error {
	sort.Strings(state.Interfaces)
	sort.Slice(
		state.Actions,
		func(i int, j int) bool {
			return state.Actions[i] < state.Actions[j]
		},
	)

	path := s.nativeSpeedLimitStatePath()

	if err := os.MkdirAll(
		filepath.Dir(path),
		0700,
	); err != nil {
		return err
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}

	return atomicWriteFile(
		path,
		raw,
		0600,
	)
}

func nativeSpeedActionSet(
	attachments []nativeSpeedAttachment,
) (map[uint32]struct{}, error) {
	result := map[uint32]struct{}{}

	for _, item := range attachments {
		if item.UploadRate > 0 {
			index, err := nativeSpeedActionIndex(
				item.UserID,
				nativeSpeedUpload,
			)
			if err != nil {
				return nil, err
			}

			result[index] = struct{}{}
		}

		if item.DownloadRate > 0 {
			index, err := nativeSpeedActionIndex(
				item.UserID,
				nativeSpeedDownload,
			)
			if err != nil {
				return nil, err
			}

			result[index] = struct{}{}
		}
	}

	return result, nil
}

func nativeSpeedActionSlice(
	actions map[uint32]struct{},
) []uint32 {
	result := make([]uint32, 0, len(actions))

	for index := range actions {
		result = append(result, index)
	}

	sort.Slice(
		result,
		func(i int, j int) bool {
			return result[i] < result[j]
		},
	)

	return result
}

func nativeSpeedInterfaceUnion(
	previous []string,
	current []string,
) []string {
	seen := map[string]struct{}{}

	for _, values := range [][]string{
		previous,
		current,
	} {
		for _, value := range values {
			if value == "" {
				continue
			}

			seen[value] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))

	for value := range seen {
		result = append(result, value)
	}

	sort.Strings(result)

	return result
}

func (s *Server) clearNativeSpeedLimits() error {
	if nativeSpeedLimitGOOS != "linux" {
		return nil
	}

	state, err := s.loadNativeSpeedLimitState()
	if err != nil {
		return err
	}

	for _, interfaceName := range state.Interfaces {
		nativeSpeedClearInterface(interfaceName)
	}

	for _, index := range state.Actions {
		nativeSpeedDeleteActionIndex(index)
	}

	err = os.Remove(s.nativeSpeedLimitStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

func (s *Server) clearNativeSpeedLimitsLogged() {
	if err := s.clearNativeSpeedLimits(); err != nil {
		s.appendLog(
			"clear native speed limits failed: " + err.Error(),
		)
	}
}

func nativeSpeedAddDesiredUserActions(
	actions map[uint32]struct{},
	userID int64,
	uploadRate int64,
	downloadRate int64,
) error {
	if uploadRate > 0 {
		index, err := nativeSpeedActionIndex(
			userID,
			nativeSpeedUpload,
		)
		if err != nil {
			return err
		}

		actions[index] = struct{}{}
	}

	if downloadRate > 0 {
		index, err := nativeSpeedActionIndex(
			userID,
			nativeSpeedDownload,
		)
		if err != nil {
			return err
		}

		actions[index] = struct{}{}
	}

	return nil
}

func nativeSpeedDeleteNewActions(
	desired map[uint32]struct{},
	previous []uint32,
) {
	keep := make(map[uint32]struct{}, len(previous))
	for _, index := range previous {
		keep[index] = struct{}{}
	}

	for index := range desired {
		if _, ok := keep[index]; ok {
			continue
		}

		nativeSpeedDeleteActionIndex(index)
	}
}
