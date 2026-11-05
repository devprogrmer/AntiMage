package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type runtimePolicySnapshot struct {
	Version    int    `json:"version"`
	ConfigJSON string `json:"config_json"`
	NativeJSON string `json:"native_json"`
	Revision   uint64 `json:"revision"`
	Checksum   string `json:"checksum"`
	Stopped    bool   `json:"stopped,omitempty"`
	// Parsed once before publication. All nested slices/maps are immutable;
	// replacing a policy (even at the same revision) builds a new snapshot.
	native *nativeRuntimePayload
}

func (snapshot *runtimePolicySnapshot) nativePayload() (nativeRuntimePayload, error) {
	if snapshot.native != nil {
		return *snapshot.native, nil
	}
	// Legacy/test-created snapshots have not passed the load/persist path.
	// Do not lazily mutate them: concurrent readers must remain race-free.
	return parseNativeRuntimePayload(snapshot.NativeJSON)
}

func runtimePolicyChecksum(snapshot runtimePolicySnapshot) (string, error) {
	snapshot.Checksum = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) runtimePolicyPath() string {
	return filepath.Join(s.cfg.DataDir, "accounting", "runtime-policy.json")
}

func (s *Server) currentRuntimePolicy() (*runtimePolicySnapshot, error) {
	s.mu.Lock()
	snapshot := s.runtimePolicy
	s.mu.Unlock()
	if snapshot != nil {
		return snapshot, nil
	}
	return s.loadRuntimePolicy()
}

func (s *Server) persistRuntimePolicy(req *nodev1.RuntimeConfigRequest) error {
	if strings.TrimSpace(req.GetConfigJson()) == "" {
		return fmt.Errorf("empty runtime policy config")
	}
	if !json.Valid([]byte(req.GetConfigJson())) {
		return fmt.Errorf("invalid runtime policy config JSON")
	}
	payload, err := parseNativeRuntimePayload(req.GetOvRuntimeJson())
	if err != nil {
		return err
	}
	snapshot := runtimePolicySnapshot{Version: 1, ConfigJSON: req.GetConfigJson(), NativeJSON: req.GetOvRuntimeJson(), Revision: req.GetDesiredRevision(), native: &payload}
	snapshot.Checksum, err = runtimePolicyChecksum(snapshot)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	path := s.runtimePolicyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := writeAccountingState(path, raw); err != nil {
		return err
	}
	s.mu.Lock()
	s.runtimePolicy = &snapshot
	s.mu.Unlock()
	return nil
}

func (s *Server) loadRuntimePolicy() (*runtimePolicySnapshot, error) {
	raw, err := readOfflineAccountingState(s.runtimePolicyPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot runtimePolicySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decode offline runtime policy: %w", err)
	}
	checksum, err := runtimePolicyChecksum(snapshot)
	if err != nil || snapshot.Version != 1 || checksum != snapshot.Checksum || strings.TrimSpace(snapshot.ConfigJSON) == "" || !json.Valid([]byte(snapshot.ConfigJSON)) {
		return nil, fmt.Errorf("offline runtime policy integrity check failed")
	}
	payload, err := parseNativeRuntimePayload(snapshot.NativeJSON)
	if err != nil {
		return nil, err
	}
	snapshot.native = &payload
	s.mu.Lock()
	s.runtimePolicy = &snapshot
	s.mu.Unlock()
	return &snapshot, nil
}

func (s *Server) restoreRuntimePolicy(ctx context.Context) error {
	snapshot, err := s.loadRuntimePolicy()
	if err != nil || snapshot == nil {
		return err
	}
	if snapshot.Stopped {
		return nil
	}
	_, err = s.applyConfig(ctx, &nodev1.RuntimeConfigRequest{ConfigJson: snapshot.ConfigJSON, OvRuntimeJson: snapshot.NativeJSON, DesiredRevision: snapshot.Revision, OperationId: "offline-policy-restore"}, "offline policy restored")
	return err
}

func (s *Server) persistStoppedRuntimePolicy() error {
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		return err
	}
	snapshot := *current
	snapshot.Stopped = true
	snapshot.Checksum, err = runtimePolicyChecksum(snapshot)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := writeAccountingState(s.runtimePolicyPath(), raw); err != nil {
		return err
	}
	s.mu.Lock()
	s.runtimePolicy = &snapshot
	s.mu.Unlock()
	return nil
}
