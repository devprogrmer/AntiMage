package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Generation must change before a counter-resetting interface or peer replacement.
// Empty generations use observed decreases/absence only, which cannot detect a
// reset followed by traffic beyond the previous counter between observations.
type amneziaWGOfflineCounter struct {
	InterfaceIdentity string `json:"interface_identity,omitempty"`
	UserID            int64  `json:"user_id"`
	InboundTag        string `json:"inbound_tag"`
	Generation        string `json:"generation"`
	Received          uint64 `json:"received"`
	Sent              uint64 `json:"sent"`
	Total             uint64 `json:"total"`
	Absent            bool   `json:"absent"`
}

type amneziaWGOfflineState struct {
	Counters  map[string]amneziaWGOfflineCounter `json:"counters"`
	Snapshots map[string][]wireGuardPeerCounters `json:"-"`
}

// Optional aggregate provider: one native client for all configured interfaces.
// Nil keeps the existing injectable single-interface provider usable.
var amneziaWGSnapshotAll = amneziaWGPlatformSnapshotAll
var amneziaWGInterfaceIdentity = amneziaWGPlatformInterfaceIdentity
var amneziaWGOfflineWrite = amneziaWGDurableWrite

// The parent owns durable root ACK credits and reflection-marker pruning.
var amneziaWGAwaitingReflectionUsage = func(s *Server, userID int64, inboundTag, pendingChildID, reflectedRootBatchID string) (uint64, error) {
	return s.localAwaitingReflectionUsage("amneziawg", userID, inboundTag, pendingChildID, reflectedRootBatchID)
}

func (s *Server) amneziaWGUnreflectedRawLocked(state amneziaWGOfflineState, userID int64, tag string) (uint64, error) {
	var total uint64
	for key, c := range state.Counters {
		if c.UserID != userID || c.InboundTag != tag {
			continue
		}
		baseline := s.amneziaWGUsageBaseline[key]
		if s.amneziaWGUsagePending != nil {
			if sent, ok := s.amneziaWGUsagePending.NextBaseline[key]; ok {
				baseline = sent
			}
		}
		if c.Total < baseline {
			return 0, fmt.Errorf("AWG quota logical counter below sent baseline")
		}
		delta := c.Total - baseline
		if ^uint64(0)-total < delta {
			return 0, fmt.Errorf("AWG quota overflow")
		}
		total += delta
	}
	// Use the immutable sent samples, including legacy pending bytes whose peer
	// has disappeared, then add only durable bytes newer than the sent snapshot.
	if pending := s.amneziaWGUsagePending; pending != nil {
		for _, sample := range pending.Samples {
			if sample.UserID != userID || sample.InboundTag != tag {
				continue
			}
			if ^uint64(0)-total < sample.Value {
				return 0, fmt.Errorf("AWG pending quota overflow")
			}
			total += sample.Value
		}
	}
	return total, nil
}

func (s *Server) readAmneziaWGOfflineState() (amneziaWGOfflineState, error) {
	state := amneziaWGOfflineState{Counters: map[string]amneziaWGOfflineCounter{}}
	raw, err := readOfflineAccountingState(filepath.Join(s.cfg.DataDir, "amneziawg", "offline-accounting.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(raw, &state)
	if state.Counters == nil {
		state.Counters = map[string]amneziaWGOfflineCounter{}
	}
	return state, err
}

func (s *Server) checkpointAmneziaWGOffline(ctx context.Context) error {
	s.amneziaWGUsageMu.Lock()
	defer s.amneziaWGUsageMu.Unlock()
	_, err := s.amneziaWGOfflineLocked(ctx, false, true)
	return err
}

func (s *Server) quotaCheckAmneziaWGOffline(ctx context.Context) error {
	s.amneziaWGUsageMu.Lock()
	defer s.amneziaWGUsageMu.Unlock()
	_, err := s.amneziaWGOfflineLocked(ctx, true, false)
	return err
}

func (s *Server) amneziaWGOfflineLocked(ctx context.Context, enforce, checkpoint bool) (amneziaWGOfflineState, error) {
	if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
		return amneziaWGOfflineState{}, err
	}
	state, err := s.readAmneziaWGOfflineState()
	if err != nil {
		return state, err
	}
	root := filepath.Join(s.cfg.DataDir, "amneziawg", "runtime")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	configs := []amneziaWGUsageRuntimeConfig{}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "usage-helper.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return state, err
		}
		var cfg amneziaWGUsageRuntimeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return state, err
		}
		if cfg.Transitioning {
			return state, fmt.Errorf("AWG %s has an interrupted generation transition; refusing ambiguous counters", cfg.InboundTag)
		}
		configs = append(configs, cfg)
		if !cfg.Stopped {
			names = append(names, cfg.InterfaceName)
		}
	}
	if len(configs) == 0 {
		return state, nil
	}
	snapshots := map[string][]wireGuardPeerCounters{}
	if len(names) == 0 {
		// Stopped helpers retain durable ownership, but have no kernel device.
	} else if amneziaWGSnapshotAll != nil {
		snapshots, err = amneziaWGSnapshotAll(ctx, names)
		if err != nil {
			return state, err
		}
	} else {
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return state, err
			}
			peers, err := amneziaWGSnapshot(name)
			if err != nil {
				return state, err
			}
			snapshots[name] = peers
		}
	}
	for _, cfg := range configs {
		peers, ok := snapshots[cfg.InterfaceName]
		if !ok {
			prefix := cfg.InboundTag + "\x00" + cfg.InterfaceName + "\x00"
			for key, c := range state.Counters {
				if strings.HasPrefix(key, prefix) {
					c.Absent = true
					state.Counters[key] = c
				}
			}
			continue
		}
		seen := map[string]bool{}
		identity, err := amneziaWGInterfaceIdentity(cfg.InterfaceName)
		if err != nil {
			return state, err
		}
		prefix := cfg.InboundTag + "\x00" + cfg.InterfaceName + "\x00"
		for _, peer := range peers {
			key := prefix + strings.TrimSpace(peer.PublicKey)
			uid := cfg.Peers[strings.TrimSpace(peer.PublicKey)]
			if uid <= 0 {
				continue
			}
			seen[key] = true
			c, exists := state.Counters[key]
			legacySeed := uint64(0)
			if !exists {
				seed := s.amneziaWGUsageBaseline[key]
				c.Total = seed
				legacySeed = seed
			}
			if exists && c.UserID != 0 && c.UserID != uid {
				return state, fmt.Errorf("AWG peer ownership changed without accounting migration")
			}
			generation := cfg.Generation + "\x00" + cfg.PeerGenerations[peer.PublicKey]
			rx, tx := peer.ReceivedBytes, peer.SentBytes
			dr, dt := rx, tx
			if !exists && legacySeed > 0 {
				// The legacy baseline stored RX+TX, without direction or identity.
				if ^uint64(0)-rx < tx {
					return state, fmt.Errorf("AWG accounting overflow")
				}
				combined := rx + tx
				if combined >= legacySeed {
					combined -= legacySeed
				}
				dr, dt = combined, 0
			}
			if exists && !c.Absent && c.Generation == generation && c.InterfaceIdentity == identity {
				if rx >= c.Received {
					dr = rx - c.Received
				}
				if tx >= c.Sent {
					dt = tx - c.Sent
				}
			}
			if ^uint64(0)-dr < dt || ^uint64(0)-c.Total < dr+dt {
				return state, fmt.Errorf("AWG accounting overflow")
			}
			if cfg.AccountingEnabled {
				c.Total += dr + dt
			}
			c.UserID, c.InboundTag, c.Generation = uid, cfg.InboundTag, generation
			c.InterfaceIdentity = identity
			c.Received, c.Sent, c.Absent = rx, tx, false
			state.Counters[key] = c
		}
		for key, c := range state.Counters {
			if strings.HasPrefix(key, prefix) && !seen[key] {
				c.Absent = true
				state.Counters[key] = c
			}
		}
	}
	state.Snapshots = snapshots
	if len(state.Counters) > maxAccountingCounterSeries {
		return state, fmt.Errorf("AWG accounting capacity exceeded")
	}
	if checkpoint {
		if err := s.persistAmneziaWGOfflineState(state); err != nil {
			return state, err
		}
	}
	persisted := checkpoint
	if enforce {
		for _, cfg := range configs {
			ipsByUser := map[int64]map[string]bool{}
			credentialsByUser := map[int64]map[string]bool{}
			for _, peer := range snapshots[cfg.InterfaceName] {
				uid := cfg.Peers[peer.PublicKey]
				if uid <= 0 {
					continue
				}
				policy, ok := cfg.Policies[peer.PublicKey]
				if !ok {
					continue
				}
				total, err := s.amneziaWGUnreflectedRawLocked(state, uid, cfg.InboundTag)
				if err != nil {
					return state, err
				}
				pendingID := ""
				if s.amneziaWGUsagePending != nil {
					pendingID = s.amneziaWGUsagePending.BatchID
				}
				reflected, err := s.localPendingUsageReflected("amneziawg", uid, pendingID, policy.ReflectedUsageBatchID)
				if err != nil {
					return state, err
				}
				if reflected && s.amneziaWGUsagePending != nil {
					for _, sample := range s.amneziaWGUsagePending.Samples {
						if sample.UserID != uid || sample.InboundTag != cfg.InboundTag {
							continue
						}
						if sample.Value > total {
							return state, fmt.Errorf("AWG reflected pending quota underflow")
						}
						total -= sample.Value
					}
				}
				credit, err := amneziaWGAwaitingReflectionUsage(s, uid, cfg.InboundTag, pendingID, policy.ReflectedUsageBatchID)
				if err != nil {
					return state, err
				}
				if ^uint64(0)-total < credit {
					return state, fmt.Errorf("AWG reflection credit overflow")
				}
				total += credit
				allowed, _ := s.localQuotaAllowed("amneziawg", uid, cfg.InboundTag, policy, total, time.Now().UTC())
				if allowed && wireGuardHandshakeActive(peer.LatestHandshake, time.Now().UTC()) {
					if credentialsByUser[uid] == nil {
						credentialsByUser[uid] = map[string]bool{}
					}
					if policy.DeviceLimit > 0 && !credentialsByUser[uid][peer.PublicKey] && int64(len(credentialsByUser[uid])) >= policy.DeviceLimit {
						allowed = false
					}
					if ipsByUser[uid] == nil {
						ipsByUser[uid] = map[string]bool{}
					}
					if allowed && offlineIPLimitExceeded(ipsByUser[uid], wireGuardEndpointHost(peer.Endpoint), policy.IPLimit) {
						allowed = false
					}
					if allowed {
						credentialsByUser[uid][peer.PublicKey] = true
					}
				}
				if !allowed {
					// One shared snapshot is persisted once, before the first removal.
					if !persisted {
						if err := s.persistAmneziaWGOfflineState(state); err != nil {
							return state, err
						}
						persisted = true
					}
					if err := amneziaWGRemovePeer(cfg.InterfaceName, peer.PublicKey); err != nil {
						return state, err
					}
				}
			}
		}
	}
	return state, nil
}

func (s *Server) persistAmneziaWGOfflineState(state amneziaWGOfflineState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := filepath.Join(s.cfg.DataDir, "amneziawg", "offline-accounting.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return amneziaWGOfflineWrite(path, raw)
}
