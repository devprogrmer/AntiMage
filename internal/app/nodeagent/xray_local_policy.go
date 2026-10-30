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

// xrayQuotaPolicyIndex is immutable except for UsageTotal, which is scratch
// storage protected by xrayUsageMu. It is keyed by the durable policy
// revision/checksum so quota ticks do not repeatedly decode NativeJSON.
type xrayQuotaPolicyIndex struct {
	Revision   uint64
	Checksum   string
	Policies   []xrayLocalUserPolicy
	EmailIndex map[string]struct{}
	UsageTotal map[string]uint64
}

func (s *Server) xrayQuotaPolicyIndexFor(snapshot *runtimePolicySnapshot) (*xrayQuotaPolicyIndex, error) {
	if snapshot == nil {
		return &xrayQuotaPolicyIndex{}, nil
	}
	cacheable := snapshot.Revision != 0 || snapshot.Checksum != ""
	if cacheable {
		s.mu.Lock()
		cached := s.xrayQuotaPolicyIndex
		if cached != nil && cached.Revision == snapshot.Revision && cached.Checksum == snapshot.Checksum {
			s.mu.Unlock()
			return cached, nil
		}
		s.mu.Unlock()
	}
	payload, err := parseNativeRuntimePayload(snapshot.NativeJSON)
	if err != nil {
		return nil, err
	}
	index := &xrayQuotaPolicyIndex{
		Revision: snapshot.Revision, Checksum: snapshot.Checksum, Policies: payload.XrayPolicies,
		EmailIndex: make(map[string]struct{}, len(payload.XrayPolicies)),
		UsageTotal: make(map[string]uint64, len(payload.XrayPolicies)),
	}
	for _, policy := range index.Policies {
		if _, exists := index.EmailIndex[policy.Email]; !exists {
			index.EmailIndex[policy.Email] = struct{}{}
		}
	}
	if cacheable {
		s.mu.Lock()
		if current := s.runtimePolicy; current != nil && current.Revision == snapshot.Revision && current.Checksum == snapshot.Checksum {
			s.xrayQuotaPolicyIndex = index
		}
		s.mu.Unlock()
	}
	return index, nil
}

// xrayQuotaUsageTotalsInto aggregates the logical counter deltas in one pass
// over Xray series, instead of rescanning the full map for every policy.
func xrayQuotaUsageTotalsInto(counters map[string]accountingCounter, baselines map[string]uint64, index *xrayQuotaPolicyIndex) error {
	clear(index.UsageTotal)
	for key, counter := range counters {
		separator := strings.LastIndexByte(key, ':')
		if separator <= 0 {
			continue
		}
		email := key[:separator]
		if _, ok := index.EmailIndex[email]; !ok {
			continue
		}
		baseline := baselines[key]
		if counter.Total < baseline {
			return fmt.Errorf("xray quota baseline exceeds logical total")
		}
		delta := counter.Total - baseline
		if ^uint64(0)-index.UsageTotal[email] < delta {
			return fmt.Errorf("xray quota overflow")
		}
		index.UsageTotal[email] += delta
	}
	return nil
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
	policyIndex, err := s.xrayQuotaPolicyIndexFor(snapshot)
	if err != nil {
		return err
	}
	if len(policyIndex.Policies) == 0 {
		return nil
	}
	client := s.cachedXrayStatsClient(path, port)
	stats, err := client.queryStats(ctx, "user>>>", false)
	if err != nil {
		return err
	}
	return s.quotaCheckXrayOfflineWithStats(ctx, snapshot, generation, policyIndex, stats)
}

func (s *Server) quotaCheckXrayOfflineWithStats(ctx context.Context, snapshot *runtimePolicySnapshot, generation string, policyIndex *xrayQuotaPolicyIndex, stats []xrayStat) error {
	if snapshot == nil || policyIndex == nil {
		return nil
	}
	s.mu.Lock()
	path, port := s.cfg.XrayPath, s.cfg.XrayAPIPort
	s.mu.Unlock()
	client := s.cachedXrayStatsClient(path, port)
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
	counters, err := s.previewXrayAccountingCountersLockedInto(stats, generation, s.xrayCounterScratch)
	if err != nil {
		return err
	}
	s.xrayCounterScratch = counters
	if err := xrayQuotaUsageTotalsInto(counters, s.xrayUsageBaseline, policyIndex); err != nil {
		return err
	}
	var enforcementErr error
	for _, policy := range policyIndex.Policies {
		raw := policyIndex.UsageTotal[policy.Email]
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
