package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type anyConnectOfflineSnapshot struct {
	Policies   map[string]nativeSessionUserPolicy
	Generation string
	Tag        string
	Config     anyConnectUsageRuntimeConfig
	Sessions   []anyConnectLiveSession
}

var anyConnectOfflineDaemonGeneration = func(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "ocserv.pid"))
	if err != nil {
		return "", err
	}
	pid := strings.TrimSpace(string(raw))
	if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
		return "", err
	}
	stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return "", err
	}
	// comm may contain spaces or parentheses. Field 22 follows the final ')'.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid ocserv process identity")
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("incomplete ocserv process identity")
	}
	return pid + ":" + fields[19], nil
}

var anyConnectOfflineQuery = func(ctx context.Context, cfg anyConnectUsageRuntimeConfig) ([]anyConnectLiveSession, error) {
	path, err := anyConnectLookPath("occtl")
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := offlineCommandOutput(anyConnectUsageCommandContext(queryCtx, path, "-s", cfg.SocketPath, "--json", "show", "users"))
	if err != nil {
		return nil, fmt.Errorf("occtl accounting snapshot: %w", err)
	}
	return parseAnyConnectUsersJSON(raw)
}

func (s *Server) anyConnectOfflineSnapshots(ctx context.Context) ([]anyConnectOfflineSnapshot, error) {
	snapshots := []anyConnectOfflineSnapshot{}
	native := map[string]anyConnectRuntimeInbound{}
	policy, err := s.currentRuntimePolicy()
	if err != nil {
		return nil, err
	}
	if policy != nil {
		payload, err := parseNativeRuntimePayload(policy.NativeJSON)
		if err != nil {
			return nil, err
		}
		for _, inbound := range payload.AnyConnectInbounds {
			native[inbound.Tag] = inbound
		}
	}
	paths, err := filepath.Glob(filepath.Join(s.cfg.DataDir, "anyconnect", "*", "usage-helper.json"))
	if err != nil {
		return nil, err
	}
	for _, helperPath := range paths {
		root := filepath.Dir(helperPath)
		raw, err := os.ReadFile(helperPath)
		if err != nil {
			return nil, err
		}
		var cfg anyConnectUsageRuntimeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		tag := strings.TrimSpace(cfg.InboundTag)
		if tag == "" || cfg.SocketPath == "" {
			return nil, fmt.Errorf("invalid persisted AnyConnect accounting config")
		}
		if policy != nil {
			if _, ok := native[tag]; !ok {
				continue
			}
		}
		generation, err := anyConnectOfflineDaemonGeneration(root)
		if err != nil {
			return nil, err
		}
		policies := map[string]nativeSessionUserPolicy{}
		policyRaw, policyErr := os.ReadFile(filepath.Join(root, "session-helper.json"))
		if policyErr == nil {
			var helper nativeSessionHelperConfig
			if err := json.Unmarshal(policyRaw, &helper); err != nil {
				return nil, err
			}
			policies = helper.Policies
		} else if !os.IsNotExist(policyErr) {
			return nil, policyErr
		}
		if inbound, ok := native[tag]; ok {
			cfg.Users = map[string]int64{}
			policies = map[string]nativeSessionUserPolicy{}
			for _, user := range inbound.Users {
				cfg.Users[user.Username] = user.UserID
				p := anyConnectSessionPolicy(user)
				p.DeviceLimit = user.DeviceLimit
				p.IPLimit = user.IPLimit
				p.ReflectedUsageBatchID = user.ReflectedUsageBatchID
				policies[user.Username] = p
			}
		}
		sessions, err := anyConnectOfflineQuery(ctx, cfg)
		if err != nil {
			return nil, err
		}
		after, err := anyConnectOfflineDaemonGeneration(root)
		if err != nil {
			return nil, err
		}
		if generation != after {
			return nil, fmt.Errorf("ocserv restarted during accounting snapshot")
		}
		snapshots = append(snapshots, anyConnectOfflineSnapshot{Generation: generation, Tag: tag, Config: cfg, Sessions: sessions, Policies: policies})
	}
	return snapshots, nil
}

func (s *Server) checkpointAnyConnectOffline(ctx context.Context) error {
	s.anyConnectUsageMu.Lock()
	defer s.anyConnectUsageMu.Unlock()
	return s.checkpointAnyConnectOfflineLocked(ctx)
}

func (s *Server) checkpointAnyConnectOfflineLocked(ctx context.Context) error {
	if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
		return err
	}
	snapshots, err := s.anyConnectOfflineSnapshots(ctx)
	if err != nil {
		return err
	}
	if len(snapshots) == 0 {
		return nil
	}
	next, err := s.previewAnyConnectOfflineLocked(snapshots)
	if err != nil {
		return err
	}
	return s.persistAnyConnectOfflinePreviewLocked(next)
}

func (s *Server) persistAnyConnectOfflinePreviewLocked(next map[string]uint64) error {
	old := s.anyConnectUsageBaseline
	s.anyConnectUsageBaseline = next
	if err := s.persistAnyConnectUsageStateLocked(); err != nil {
		s.anyConnectUsageBaseline = old
		return err
	}
	return nil
}

func (s *Server) previewAnyConnectOfflineLocked(snapshots []anyConnectOfflineSnapshot) (map[string]uint64, error) {
	boot, err := offlineReadBootID()
	if err != nil {
		return nil, err
	}
	observations := []offlineUsageObservation{}
	live := map[string]ikev2UsageSample{}
	for _, snapshot := range snapshots {
		for _, session := range snapshot.Sessions {
			userID := snapshot.Config.Users[session.Username]
			if userID <= 0 {
				continue
			}
			// Full session identity is mandatory. IP addresses identify endpoints, not devices.
			if session.GenerationID == "" {
				return nil, fmt.Errorf("occtl session lacks accounting identity")
			}
			if ^uint64(0)-session.Received < session.Sent {
				return nil, fmt.Errorf("AnyConnect native counter overflow")
			}
			owner := offlineUsageOwner{userID, snapshot.Tag}
			observations = append(observations, offlineUsageObservation{
				Generation: offlineGeneration(boot, snapshot.Generation, snapshot.Tag, session.Username, session.GenerationID, session.DisconnectID, session.StartedAt), Legacy: snapshot.Tag + "|" + session.ID,
				Owner: owner, Native: session.Received + session.Sent,
			})
			key := offlineOwnerKey(owner)
			sample := live[key]
			sample.UserID = userID
			sample.InboundTag = snapshot.Tag
			sample.Online = true
			if session.ClientIP != "" {
				sample.IPs = appendUniqueString(sample.IPs, session.ClientIP)
			}
			sample.Upload = ikev2SafeAdd(sample.Upload, session.RXSpeed)
			sample.Download = ikev2SafeAdd(sample.Download, session.TXSpeed)
			live[key] = sample
		}
	}
	legacy := s.anyConnectUsageBaseline
	if p := s.anyConnectUsagePending; p != nil && p.NextBaseline[offlineBatchMarker] != 1 {
		legacy = p.NextBaseline
	}
	compacted, err := compactOfflineBoot(s.anyConnectUsageBaseline, boot)
	if err != nil {
		return nil, err
	}
	next, err := advanceOfflineUsage(compacted, legacy, observations)
	if err != nil {
		return nil, err
	}
	if err := offlineReplaceOnline(next, live); err != nil {
		return nil, err
	}

	return next, nil
}

func (s *Server) quotaCheckAnyConnectOffline(ctx context.Context) error {
	s.anyConnectUsageMu.Lock()
	defer s.anyConnectUsageMu.Unlock()
	if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
		return err
	}
	snapshots, err := s.anyConnectOfflineSnapshots(ctx)
	if err != nil {
		return err
	}
	if len(snapshots) == 0 {
		return nil
	}
	next, err := s.previewAnyConnectOfflineLocked(snapshots)
	if err != nil {
		return err
	}
	persisted := false
	ipsByUser := map[int64]map[string]bool{}
	devicesByUser := map[int64]int64{}
	for _, snapshot := range snapshots {
		sessions := append([]anyConnectLiveSession(nil), snapshot.Sessions...)
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].StartedAt+"\x00"+sessions[i].DisconnectID < sessions[j].StartedAt+"\x00"+sessions[j].DisconnectID
		})
		for _, session := range sessions {
			id := snapshot.Config.Users[session.Username]
			if id <= 0 {
				continue
			}
			policy, ok := snapshot.Policies[session.Username]
			if !ok {
				return fmt.Errorf("AnyConnect offline quota missing persisted policy for user %d", id)
			}
			owner := offlineUsageOwner{id, snapshot.Tag}
			var captured map[string]uint64
			samples := []ikev2UsageSample{}
			pendingID := ""
			if p := s.anyConnectUsagePending; p != nil {
				captured = p.NextBaseline
				pendingID = p.BatchID
				for _, sample := range p.Samples {
					samples = append(samples, ikev2UsageSample{UserID: sample.UserID, InboundTag: sample.InboundTag, Value: sample.Value})
				}
			}
			raw, err := s.offlineQuotaRaw("anyconnect", owner, next, captured, samples, pendingID, policy.ReflectedUsageBatchID)
			if err != nil {
				return err
			}
			allowed, _ := s.localQuotaAllowed("anyconnect", id, snapshot.Tag, policy, raw, time.Now())
			if allowed && policy.DeviceLimit > 0 && devicesByUser[id] >= policy.DeviceLimit {
				allowed = false
			}
			if allowed {
				if ipsByUser[id] == nil {
					ipsByUser[id] = map[string]bool{}
				}
				allowed = !offlineIPLimitExceeded(ipsByUser[id], session.ClientIP, policy.IPLimit)
			}
			if allowed {
				devicesByUser[id]++
				continue
			}
			if session.DisconnectID == "" {
				return fmt.Errorf("AnyConnect quota session lacks identity")
			}
			if !persisted {
				if err := s.persistAnyConnectOfflinePreviewLocked(next); err != nil {
					return err
				}
				persisted = true
			}
			if err := anyConnectOfflineDisconnect(ctx, snapshot.Config, session.DisconnectID); err != nil {
				return err
			}
		}
	}
	s.anyConnectUsageBaseline = next
	return nil
}

var anyConnectOfflineDisconnect = func(ctx context.Context, cfg anyConnectUsageRuntimeConfig, id string) error {
	path, err := anyConnectLookPath("occtl")
	if err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := anyConnectUsageCommandContext(commandCtx, path, "-s", cfg.SocketPath, "disconnect", "id", id).Output(); err != nil {
		return fmt.Errorf("AnyConnect quota disconnect: %w", err)
	}
	return nil
}

func (s *Server) collectAnyConnectUserUsage(ctx context.Context, _ *nodev1.CollectUsageRequest) (*nodev1.UserUsageBatch, error) {
	s.anyConnectUsageMu.Lock()
	defer s.anyConnectUsageMu.Unlock()
	if err := s.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.anyConnectUsagePending != nil {
		return anyConnectUsageBatchProto(s.anyConnectUsagePending), nil
	}
	sampleErr := s.checkpointAnyConnectOfflineLocked(ctx)
	samples, captured, err := offlineSamples(s.anyConnectUsageBaseline)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		if sampleErr != nil {
			return nil, sampleErr
		}
		return &nodev1.UserUsageBatch{}, nil
	}
	if sampleErr != nil {
		s.appendLog("anyconnect offline checkpoint degraded: " + sampleErr.Error())
		for i := range samples {
			samples[i].Online = false
			samples[i].IPs = nil
			samples[i].Upload = 0
			samples[i].Download = 0
		}
	}
	converted := make([]anyConnectUsageSample, 0, len(samples))
	for _, sample := range samples {
		converted = append(converted, anyConnectUsageSample{UserID: sample.UserID, InboundTag: sample.InboundTag, Value: sample.Value, Online: sample.Online, IPs: sample.IPs, Upload: sample.Upload, Download: sample.Download})
	}
	p := &anyConnectUsagePendingBatch{SeenUnix: time.Now().Unix(), BatchID: fmt.Sprintf("anyconnect-%d", time.Now().UnixNano()), Samples: converted, NextBaseline: captured}
	s.anyConnectUsagePending = p
	if err := s.persistAnyConnectUsageStateLocked(); err != nil {
		s.anyConnectUsagePending = nil
		return nil, err
	}
	return anyConnectUsageBatchProto(p), nil
}
