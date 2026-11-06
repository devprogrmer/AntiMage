package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/xrayconfig"
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
	payload, err := snapshot.nativePayload()
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
			Protocol string                     `json:"protocol"`
			Settings map[string]json.RawMessage `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return err
	}
	emails := map[string]bool{}
	for _, inbound := range config.Inbounds {
		_, clients, err := xrayEffectiveConfigClients(inbound.Settings, inbound.Protocol)
		if err != nil {
			return err
		}
		for _, raw := range clients {
			var client struct {
				Email string `json:"email"`
			}
			if err := json.Unmarshal(raw, &client); err != nil {
				return err
			}
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
	usageLocked := true
	defer func() {
		if usageLocked {
			s.xrayUsageMu.Unlock()
		}
	}()
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
	restartEmails := make(map[string]struct{})
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
				restartEmails[policy.Email] = struct{}{}
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
		restartEmails[policy.Email] = struct{}{}
	}
	if len(restartEmails) > 0 {
		s.xrayUsageMu.Unlock()
		usageLocked = false
		if err := s.restartXrayAfterQuotaRevocation(ctx, snapshot, generation, restartEmails); err != nil {
			enforcementErr = errors.Join(enforcementErr, fmt.Errorf("restart xray after quota revocation: %w", err))
		}
	}
	return enforcementErr
}

// restartXrayAfterQuotaRevocation persists the quota-filtered config before
// restarting Xray. Upstream Xray has no per-user active-stream close API, so
// this closes all current Xray streams once when one or more users exhaust quota.
func (s *Server) restartXrayAfterQuotaRevocation(ctx context.Context, snapshot *runtimePolicySnapshot, generation string, emails map[string]struct{}) error {
	s.xrayTransitionMu.Lock()
	defer s.xrayTransitionMu.Unlock()
	s.mu.Lock()
	if s.lastRuntime == nil || s.xrayRuntimeGeneration != generation || s.runtimePolicy != snapshot {
		s.mu.Unlock()
		return fmt.Errorf("xray runtime or policy changed before quota cutoff restart")
	}
	configPath := s.lastConfig
	s.mu.Unlock()
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("xray runtime config path is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	configJSON, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	filtered, removed, err := removeXrayConfigClients(string(configJSON), emails)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("quota-revoked Xray clients were absent from runtime config")
	}
	if err := writeAccountingState(configPath, []byte(filtered)); err != nil {
		return fmt.Errorf("persist quota-filtered Xray config: %w", err)
	}
	// Revocation is durable now. An expired sampling context must not leave
	// the previous core serving streams after this cutoff commit point.
	if err := s.startXray(context.Background(), configPath); err != nil {
		return err
	}
	if snapshot != nil {
		if err := s.markRestoredXrayRevocations(snapshot.NativeJSON, filtered); err != nil {
			return fmt.Errorf("record Xray quota revocations after restart: %w", err)
		}
	}
	s.appendLog("xray restarted after quota cutoff; existing Xray connections were disconnected")
	return nil
}

func removeXrayConfigClients(configJSON string, emails map[string]struct{}) (string, bool, error) {
	return filterXrayConfigClients(configJSON, emails, nil)
}

// Match the core's non-nil clients precedence over the legacy users alias.
// Use the same interpretation for filtering and restored-revocation tracking.
func xrayEffectiveConfigClients(settings map[string]json.RawMessage, protocol string) (string, []json.RawMessage, error) {
	var clients []json.RawMessage
	field := "clients"
	if len(settings[field]) > 0 {
		if err := json.Unmarshal(settings[field], &clients); err != nil {
			return "", nil, err
		}
	}
	if clients == nil && (strings.EqualFold(protocol, "shadowsocks") || strings.EqualFold(protocol, "hysteria")) {
		field = "users"
		if len(settings[field]) > 0 {
			if err := json.Unmarshal(settings[field], &clients); err != nil {
				return "", nil, err
			}
		}
	}
	return field, clients, nil
}

// A managed Hysteria validator must remain the only authentication authority,
// including the instant after RemoveUser empties it, before a config restart.
func clearManagedHysteriaSharedAuth(inbound map[string]json.RawMessage) (bool, error) {
	var stream map[string]json.RawMessage
	if len(inbound["streamSettings"]) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(inbound["streamSettings"], &stream); err != nil {
		return false, err
	}
	if len(stream["hysteriaSettings"]) == 0 {
		return false, nil
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(stream["hysteriaSettings"], &settings); err != nil {
		return false, err
	}
	if len(settings["auth"]) == 0 {
		return false, nil
	}
	var auth string
	if err := json.Unmarshal(settings["auth"], &auth); err != nil {
		return false, err
	}
	if auth == "" {
		return false, nil
	}
	delete(settings, "auth")
	encoded, err := json.Marshal(settings)
	if err != nil {
		return false, err
	}
	stream["hysteriaSettings"] = encoded
	encoded, err = json.Marshal(stream)
	if err != nil {
		return false, err
	}
	inbound["streamSettings"] = encoded
	return true, nil
}

func filterXrayConfigClients(configJSON string, emails, managedTags map[string]struct{}) (string, bool, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return "", false, err
	}
	var inbounds []map[string]json.RawMessage
	if err := json.Unmarshal(config["inbounds"], &inbounds); err != nil {
		return "", false, err
	}
	removed := false
	keptInbounds := make([]map[string]json.RawMessage, 0, len(inbounds))
	for _, inbound := range inbounds {
		var protocol, tag string
		if err := json.Unmarshal(inbound["protocol"], &protocol); err != nil {
			return "", false, err
		}
		if len(inbound["tag"]) > 0 {
			if err := json.Unmarshal(inbound["tag"], &tag); err != nil {
				return "", false, err
			}
		}
		keptInbounds = append(keptInbounds, inbound)
		if _, managed := managedTags[tag]; managed && strings.EqualFold(protocol, "hysteria") {
			changed, err := clearManagedHysteriaSharedAuth(inbound)
			if err != nil {
				return "", false, err
			}
			removed = removed || changed
		}
		var settings map[string]json.RawMessage
		if len(inbound["settings"]) > 0 {
			if err := json.Unmarshal(inbound["settings"], &settings); err != nil {
				return "", false, err
			}
		}
		field, clients, err := xrayEffectiveConfigClients(settings, protocol)
		if err != nil {
			return "", false, err
		}
		kept := make([]json.RawMessage, 0, len(clients))
		for _, client := range clients {
			var identity struct {
				Email string `json:"email"`
			}
			if err := json.Unmarshal(client, &identity); err != nil {
				return "", false, err
			}
			if _, revoke := emails[identity.Email]; revoke {
				removed = true
				continue
			}
			kept = append(kept, client)
		}
		_, managed := managedTags[tag]
		if len(kept) == 0 && (managed || len(kept) != len(clients)) {
			var method string
			if len(settings["method"]) > 0 {
				if err := json.Unmarshal(settings["method"], &method); err != nil {
					return "", false, err
				}
			}
			if xrayconfig.EmptyUsersAllowServerAuth(protocol, method) {
				keptInbounds = keptInbounds[:len(keptInbounds)-1]
				removed = true
				continue
			}
		}
		if len(kept) != len(clients) {
			encoded, err := json.Marshal(kept)
			if err != nil {
				return "", false, err
			}
			settings[field] = encoded
			encoded, err = json.Marshal(settings)
			if err != nil {
				return "", false, err
			}
			inbound["settings"] = encoded
		}
	}
	if !removed {
		return configJSON, false, nil
	}
	encodedInbounds, err := json.Marshal(keptInbounds)
	if err != nil {
		return "", false, err
	}
	config["inbounds"] = encodedInbounds
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}
