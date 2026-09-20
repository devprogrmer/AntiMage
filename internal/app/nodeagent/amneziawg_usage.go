package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type amneziaWGUsageRuntimeConfig struct {
	InboundTag        string                             `json:"inbound_tag"`
	InterfaceName     string                             `json:"interface_name"`
	Peers             map[string]int64                   `json:"peers"`
	PeerAddresses     map[string]string                  `json:"peer_addresses"`
	Policies          map[string]nativeSessionUserPolicy `json:"policies"`
	AccountingEnabled bool                               `json:"accounting_enabled"`
	Callback          nativeRuntimeSessionCallback       `json:"session_callback,omitempty"`
}

type amneziaWGUsageSample struct {
	UserID     int64  `json:"user_id"`
	InboundTag string `json:"inbound_tag"`
	Value      uint64 `json:"value"`
}
type amneziaWGUsagePendingBatch struct {
	BatchID      string                 `json:"batch_id"`
	Samples      []amneziaWGUsageSample `json:"samples"`
	NextBaseline map[string]uint64      `json:"next_baseline"`
}
type amneziaWGUsageDiskState struct {
	Baseline         map[string]uint64           `json:"baseline"`
	Pending          *amneziaWGUsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                      `json:"last_acked_batch_id,omitempty"`
}

var amneziaWGSnapshot = amneziaWGPlatformSnapshot
var amneziaWGRemovePeer = amneziaWGPlatformRemovePeer

func (s *Server) amneziaWGUsageStatePath() string {
	return filepath.Join(s.cfg.DataDir, "amneziawg", "usage-state.json")
}
func (s *Server) ensureAmneziaWGUsageStateLoadedLocked() error {
	if s.amneziaWGUsageLoaded {
		return nil
	}
	raw, err := os.ReadFile(s.amneziaWGUsageStatePath())
	if os.IsNotExist(err) {
		s.amneziaWGUsageLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var state amneziaWGUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Baseline != nil {
		s.amneziaWGUsageBaseline = state.Baseline
	}
	s.amneziaWGUsagePending = state.Pending
	s.amneziaWGUsageLastAckedBatchID = state.LastAckedBatchID
	s.amneziaWGUsageLoaded = true
	return nil
}
func (s *Server) persistAmneziaWGUsageStateLocked() error {
	raw, err := json.Marshal(amneziaWGUsageDiskState{Baseline: s.amneziaWGUsageBaseline, Pending: s.amneziaWGUsagePending, LastAckedBatchID: s.amneziaWGUsageLastAckedBatchID})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.amneziaWGUsageStatePath()), 0700); err != nil {
		return err
	}
	return atomicWriteFile(s.amneziaWGUsageStatePath(), raw, 0600)
}

func (s *Server) collectAmneziaWGUserUsage(ctx context.Context, _ *nodev1.CollectUsageRequest) (*nodev1.UserUsageBatch, error) {
	s.amneziaWGUsageMu.Lock()
	defer s.amneziaWGUsageMu.Unlock()
	if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.amneziaWGUsagePending != nil {
		return amneziaWGUsageBatchProto(s.amneziaWGUsagePending, nil), nil
	}
	next := map[string]uint64{}
	for key, value := range s.amneziaWGUsageBaseline {
		next[key] = value
	}
	type aggregateKey struct {
		userID  int64
		inbound string
	}
	aggregated := map[aggregateKey]uint64{}
	onlineIPs := []*nodev1.OnlineUserIP{}
	entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "amneziawg", "runtime"))
	if os.IsNotExist(err) {
		return &nodev1.UserUsageBatch{}, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(s.cfg.DataDir, "amneziawg", "runtime", entry.Name(), "usage-helper.json"))
		if readErr != nil {
			continue
		}
		var cfg amneziaWGUsageRuntimeConfig
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		peers, snapErr := amneziaWGSnapshot(cfg.InterfaceName)
		if snapErr != nil {
			s.appendLog("amneziawg stats query failed: " + snapErr.Error())
			continue
		}
		for _, peer := range peers {
			publicKey := strings.TrimSpace(peer.PublicKey)
			userID := cfg.Peers[publicKey]
			if userID <= 0 {
				continue
			}
			counter := peer.ReceivedBytes + peer.SentBytes
			baselineKey := cfg.InboundTag + "\x00" + cfg.InterfaceName + "\x00" + publicKey
			baseline, exists := s.amneziaWGUsageBaseline[baselineKey]
			delta := counter
			if exists && counter >= baseline {
				delta = counter - baseline
			}
			next[baselineKey] = counter
			if cfg.AccountingEnabled && delta > 0 {
				aggregated[aggregateKey{userID, cfg.InboundTag}] += delta
			}
			if peer.LatestHandshake > 0 && now.Sub(time.Unix(peer.LatestHandshake, 0)) <= 3*time.Minute {
				address := cfg.PeerAddresses[publicKey]
				if host, _, splitErr := net.SplitHostPort(peer.Endpoint); splitErr == nil && host != "" {
					address = host
				}
				onlineIPs = append(onlineIPs, &nodev1.OnlineUserIP{Uid: "amneziawg:" + strconv.FormatInt(userID, 10), Ips: []*nodev1.OnlineIP{{Ip: address, LastSeenUnix: now.Unix()}}})
				event := amneziaWGSessionEvent(cfg, peer, userID, "seen")
				if eventErr := s.sendNativeSessionEvent(ctx, cfg.Callback, event); errors.Is(eventErr, errNativeSessionDeviceLimit) {
					_ = amneziaWGRemovePeer(cfg.InterfaceName, publicKey)
				}
			} else {
				_ = s.sendNativeSessionEvent(ctx, cfg.Callback, amneziaWGSessionEvent(cfg, peer, userID, "stop"))
			}
			policy := cfg.Policies[publicKey]
			allowed, _ := nativeSessionUserPolicyAllowed(policy, now)
			if allowed && policy.DataLimit > 0 && policy.UsedTraffic+int64(delta) >= policy.DataLimit {
				_ = amneziaWGRemovePeer(cfg.InterfaceName, publicKey)
			}
		}
	}
	keys := make([]aggregateKey, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].userID == keys[j].userID {
			return keys[i].inbound < keys[j].inbound
		}
		return keys[i].userID < keys[j].userID
	})
	samples := make([]amneziaWGUsageSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, amneziaWGUsageSample{UserID: key.userID, InboundTag: key.inbound, Value: aggregated[key]})
	}
	pending := &amneziaWGUsagePendingBatch{BatchID: fmt.Sprintf("amneziawg-%d", now.UnixNano()), Samples: samples, NextBaseline: next}
	s.amneziaWGUsagePending = pending
	if err := s.persistAmneziaWGUsageStateLocked(); err != nil {
		s.amneziaWGUsagePending = nil
		return nil, err
	}
	return amneziaWGUsageBatchProto(pending, onlineIPs), nil
}

func amneziaWGSessionEvent(cfg amneziaWGUsageRuntimeConfig, peer wireGuardPeerCounters, userID int64, event string) nativeSessionEvent {
	publicKey := strings.TrimSpace(peer.PublicKey)
	return nativeSessionEvent{UserID: userID, Protocol: "amneziawg", InboundTag: strings.TrimSpace(cfg.InboundTag), SessionID: wireGuardSessionID(cfg.InboundTag, publicKey), AssignedIP: strings.TrimSpace(cfg.PeerAddresses[publicKey]), ClientIP: wireGuardEndpointHost(peer.Endpoint), DeviceID: wireGuardSafeDeviceID(publicKey), DeviceType: "Unknown", ClientName: "AmneziaWG", Platform: "Unknown", Event: event}
}

func amneziaWGUsageBatchProto(pending *amneziaWGUsagePendingBatch, onlineIPs []*nodev1.OnlineUserIP) *nodev1.UserUsageBatch {
	stats := []*nodev1.UserUsageSample{}
	batchID := ""
	if pending != nil {
		batchID = pending.BatchID
		for _, sample := range pending.Samples {
			stats = append(stats, &nodev1.UserUsageSample{Uid: "amneziawg:" + strconv.FormatInt(sample.UserID, 10), InboundTag: sample.InboundTag, Value: sample.Value})
		}
	}
	for _, item := range onlineIPs {
		id := strings.TrimPrefix(item.Uid, "amneziawg:")
		stats = append(stats, &nodev1.UserUsageSample{Uid: "online:amneziawg:" + id, Value: 0})
	}
	return &nodev1.UserUsageBatch{BatchId: batchID, Stats: stats, OnlineIps: onlineIPs}
}

func (s *Server) ackAmneziaWGUserUsage(_ context.Context, req *nodev1.AckUsageRequest) (*nodev1.AckUsageResponse, error) {
	s.amneziaWGUsageMu.Lock()
	defer s.amneziaWGUsageMu.Unlock()
	if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetBatchId())
	if id == s.amneziaWGUsageLastAckedBatchID {
		return &nodev1.AckUsageResponse{Acknowledged: true}, nil
	}
	if s.amneziaWGUsagePending == nil || s.amneziaWGUsagePending.BatchID != id {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}
	old := s.amneziaWGUsageBaseline
	pending := s.amneziaWGUsagePending
	s.amneziaWGUsageBaseline = pending.NextBaseline
	s.amneziaWGUsagePending = nil
	s.amneziaWGUsageLastAckedBatchID = id
	if err := s.persistAmneziaWGUsageStateLocked(); err != nil {
		s.amneziaWGUsageBaseline = old
		s.amneziaWGUsagePending = pending
		return nil, err
	}
	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}
