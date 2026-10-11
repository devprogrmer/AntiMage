package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type xrayLocalUserPolicy struct {
	UserID      int64  `json:"user_id"`
	Email       string `json:"email"`
	InboundTag  string `json:"inbound_tag"`
	Protocol    string `json:"protocol"`
	DeviceLimit int64  `json:"device_limit"`
	IPLimit     int64  `json:"ip_limit"`
	nativeSessionUserPolicy
}

type xrayQuotaRevocation struct {
	Generation      string
	ExistingStreams bool
}

func (s *Server) markRestoredXrayRevocations(nativeJSON, configJSON string) error {
	payload, err := parseNativeRuntimePayload(nativeJSON)
	if err != nil {
		return err
	}
	var config struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return err
	}
	emails := map[string]bool{}
	for _, inbound := range config.Inbounds {
		for _, client := range inbound.Settings.Clients {
			emails[client.Email] = true
		}
	}
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	s.mu.Lock()
	generation := s.xrayRuntimeGeneration
	s.mu.Unlock()
	s.xrayQuotaRevoked = map[string]xrayQuotaRevocation{}
	for _, policy := range payload.XrayPolicies {
		if !emails[policy.Email] {
			s.xrayQuotaRevoked[policy.Email] = xrayQuotaRevocation{Generation: generation}
		}
	}
	return nil
}

func (s *Server) quotaCheckXrayOffline(ctx context.Context) error {
	s.mu.Lock()
	snapshot := s.runtimePolicy
	running := s.lastRuntime != nil
	path, port, generation := s.cfg.XrayPath, s.cfg.XrayAPIPort, s.xrayRuntimeGeneration
	s.mu.Unlock()
	if snapshot == nil || !running {
		return nil
	}
	payload, err := parseNativeRuntimePayload(snapshot.NativeJSON)
	if err != nil {
		return err
	}
	if len(payload.XrayPolicies) == 0 {
		return nil
	}
	client := s.cachedXrayStatsClient(path, port)
	stats, err := client.queryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return err
	}
	s.mu.Lock()
	unchanged := generation == s.xrayRuntimeGeneration
	s.mu.Unlock()
	if !unchanged {
		return fmt.Errorf("xray changed during quota snapshot")
	}
	counters, err := s.previewXrayAccountingCountersLocked(stats, generation)
	if err != nil {
		return err
	}
	var enforcementErr error
	for _, policy := range payload.XrayPolicies {
		var raw uint64
		for key, counter := range counters {
			if !strings.HasPrefix(key, policy.Email+":") {
				continue
			}
			baseline := s.xrayUsageBaseline[key]
			if counter.Total < baseline {
				return fmt.Errorf("xray quota baseline exceeds logical total")
			}
			delta := counter.Total - baseline
			if ^uint64(0)-raw < delta {
				return fmt.Errorf("xray quota overflow")
			}
			raw += delta
		}
		pendingID := ""
		if p := s.xrayUsagePending; p != nil {
			pendingID = p.BatchID
			reflected, err := s.localPendingUsageReflected("xray", policy.UserID, pendingID, policy.ReflectedUsageBatchID)
			if err != nil {
				return err
			}
			if reflected {
				for _, sample := range p.Samples {
					if sample.UserID != policy.UserID || sample.InboundTag != policy.InboundTag {
						continue
					}
					if sample.Value > raw {
						return fmt.Errorf("xray reflected pending exceeds unacked usage")
					}
					raw -= sample.Value
				}
			}
		}
		awaiting, err := s.localAwaitingReflectionUsage("xray", policy.UserID, policy.InboundTag, pendingID, policy.ReflectedUsageBatchID)
		if err != nil {
			return err
		}
		if ^uint64(0)-raw < awaiting {
			return fmt.Errorf("xray reflection overflow")
		}
		allowed, _ := s.localQuotaAllowed("xray", policy.UserID, policy.InboundTag, policy.nativeSessionUserPolicy, raw+awaiting, time.Now())
		if allowed {
			continue
		}
		if revoked, ok := s.xrayQuotaRevoked[policy.Email]; ok && revoked.Generation == generation {
			if revoked.ExistingStreams {
				enforcementErr = errors.Join(enforcementErr, fmt.Errorf("xray %s authentication revoked; existing-stream termination unavailable", policy.Email))
			}
			continue
		}
		if err := s.checkpointXrayGenerationLocked(stats, generation); err != nil {
			return err
		}
		if err := client.removeUserRPC(ctx, policy.InboundTag, policy.Email); err != nil {
			enforcementErr = errors.Join(enforcementErr, fmt.Errorf("xray remove exhausted user: %w", err))
			continue
		}
		if s.xrayQuotaRevoked == nil {
			s.xrayQuotaRevoked = map[string]xrayQuotaRevocation{}
		}
		s.xrayQuotaRevoked[policy.Email] = xrayQuotaRevocation{Generation: generation, ExistingStreams: true}
		// Native removal revokes authentication, but upstream Xray exposes no
		// per-user close-all-streams API. Do not claim hard cutoff of old streams.
		enforcementErr = errors.Join(enforcementErr, fmt.Errorf("xray %s authentication revoked; existing-stream termination unavailable", policy.Email))
	}
	return enforcementErr
}
