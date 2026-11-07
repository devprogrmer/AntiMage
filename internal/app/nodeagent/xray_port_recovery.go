package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// Caller holds xrayTransitionMu. The durable policy and the running core's
// config must describe the same applied revision before it can be recovered.
func (s *Server) xrayPortRecoveryPolicy(req *nodev1.RuntimeConfigRequest) *runtimePolicySnapshot {
	s.mu.Lock()
	previous, applied := s.runtimePolicy, s.appliedRev
	running, configPath := s.lastRuntime != nil, s.lastConfig
	s.mu.Unlock()
	if !running || previous == nil || previous.Stopped || previous.Revision != applied ||
		previous.NativeJSON != req.GetOvRuntimeJson() || previous.ConfigJSON == req.GetConfigJson() {
		return nil
	}
	if !xraySingleInboundPortChange(previous.ConfigJSON, req.GetConfigJson()) {
		return nil
	}
	raw, err := os.ReadFile(configPath)
	if err != nil || string(raw) != previous.ConfigJSON {
		return nil
	}
	return previous
}

// Accept one numeric inbound port edit. Compare the compact JSON as well as
// the decoded port so other values, key ordering and aliases cannot change.
func xraySingleInboundPortChange(previous, next string) bool {
	type config struct {
		Inbounds []struct {
			Port json.RawMessage `json:"port"`
		} `json:"inbounds"`
	}
	var oldConfig, newConfig config
	if json.Unmarshal([]byte(previous), &oldConfig) != nil || json.Unmarshal([]byte(next), &newConfig) != nil ||
		len(oldConfig.Inbounds) != len(newConfig.Inbounds) {
		return false
	}
	for i, inbound := range oldConfig.Inbounds {
		updated := newConfig.Inbounds[i]
		if bytes.Equal(inbound.Port, updated.Port) {
			continue
		}
		var oldPort, newPort int
		if json.Unmarshal(inbound.Port, &oldPort) != nil || json.Unmarshal(updated.Port, &newPort) != nil ||
			oldPort < 1 || oldPort > 65535 || newPort < 1 || newPort > 65535 || oldPort == newPort {
			return false
		}
		var oldJSON, newJSON bytes.Buffer
		if json.Compact(&oldJSON, []byte(previous)) != nil || json.Compact(&newJSON, []byte(next)) != nil {
			return false
		}
		return strings.Replace(oldJSON.String(), fmt.Sprintf(`"port":%d`, oldPort), fmt.Sprintf(`"port":%d`, newPort), 1) == newJSON.String()
	}
	return false
}

func (s *Server) recoverXrayPortChange(configPath string, previous *runtimePolicySnapshot) error {
	// Reevaluate local quota and expiry after the failed start. The durable
	// policy still points to the previous revision and offline startup does
	// the same evaluation.
	req, err := s.guardOfflineRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson: previous.ConfigJSON, OvRuntimeJson: previous.NativeJSON,
	})
	if err != nil {
		return fmt.Errorf("guard recovered Xray config: %w", err)
	}
	if err := writeAccountingState(configPath, []byte(req.GetConfigJson())); err != nil {
		return fmt.Errorf("write recovered Xray config: %w", err)
	}
	if _, _, err := s.syncXrayAPIPortFromRuntimeConfig(req.GetConfigJson()); err != nil {
		return err
	}
	if err := s.startXray(context.Background(), configPath); err != nil {
		return err
	}
	return s.markRestoredXrayRevocations(req.GetOvRuntimeJson(), req.GetConfigJson())
}
