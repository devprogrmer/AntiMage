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

	"github.com/antimage/antimage/internal/app/online"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type wireGuardUsageSample struct {
	UserID     int64
	InboundTag string
	Value      uint64
}

type wireGuardUsagePendingBatch struct {
	BatchID      string
	Samples      []wireGuardUsageSample
	NextBaseline map[string]uint64
	CarryValues  map[string]uint64
}

func (s *Server) collectWireGuardUserUsage(
	ctx context.Context,
	_ *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()

	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	pendingAccounting := s.wireGuardUsagePending
	if s.wireGuardUsageBaseline == nil {
		s.wireGuardUsageBaseline = make(map[string]uint64)
	}

	nextBaseline := make(
		map[string]uint64,
		len(s.wireGuardUsageBaseline),
	)
	for key, value := range s.wireGuardUsageBaseline {
		nextBaseline[key] = value
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}
	aggregated := make(map[aggregateKey]wireGuardUsageSample)
	onlineUsers := make(map[int64]struct{})
	configuredKeys := make(map[string]struct{})
	carryValues := make(map[string]uint64)
	if pendingAccounting == nil {
		for baselineKey, carry := range s.wireGuardUsageCarry {
			if carry.UserID <= 0 ||
				strings.TrimSpace(carry.InboundTag) == "" ||
				carry.Value == 0 {
				continue
			}

			key := aggregateKey{
				UserID:     carry.UserID,
				InboundTag: strings.TrimSpace(carry.InboundTag),
			}
			sample := aggregated[key]
			sample.UserID = carry.UserID
			sample.InboundTag = key.InboundTag
			if ^uint64(0)-sample.Value < carry.Value {
				return nil, fmt.Errorf(
					"wireguard carry aggregate overflow for user %d",
					carry.UserID,
				)
			}
			sample.Value += carry.Value
			aggregated[key] = sample

			nextBaseline[baselineKey] = carry.NextBaseline
			configuredKeys[baselineKey] = struct{}{}
			carryValues[baselineKey] = carry.Value
		}
	}
	baselinePruningSafe := true
	observedAt := time.Now().UTC()

	var allDump map[string]wireGuardInterfaceDump
	allDumpLoaded := false

	root := filepath.Join(s.cfg.DataDir, "wireguard", "inbounds")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing helper state must not discard pending or carried usage.
			// It is also unsafe to prune baselines without a complete config view.
			entries = nil
			baselinePruningSafe = false
		} else {
			return nil, fmt.Errorf("list wireguard usage configs: %w", err)
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		path := filepath.Join(root, entry.Name(), "usage-helper.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			baselinePruningSafe = false
			s.appendLog(
				"wireguard usage config read failed for " +
					entry.Name() + ": " + err.Error(),
			)
			continue
		}

		var cfg wireGuardUsageRuntimeConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			baselinePruningSafe = false
			s.appendLog(
				"wireguard usage config parse failed for " +
					entry.Name() + ": " + err.Error(),
			)
			continue
		}

		cfg.InboundTag = strings.TrimSpace(cfg.InboundTag)
		cfg.InterfaceName = strings.TrimSpace(cfg.InterfaceName)
		if cfg.InboundTag == "" {
			baselinePruningSafe = false
			continue
		}

		interfaceName := cfg.InterfaceName
		var peers []wireGuardPeerCounters

		if interfaceName != "" {
			rawDump, err := wireGuardDumpInterface(ctx, interfaceName)
			if err != nil {
				baselinePruningSafe = false
				s.appendLog(
					"wireguard stats query failed for " +
						cfg.InboundTag + " (" + interfaceName +
						"): " + err.Error(),
				)
				continue
			}
			peers, err = parseWireGuardDump(string(rawDump))
			if err != nil {
				baselinePruningSafe = false
				s.appendLog(
					"wireguard stats parse failed for " +
						cfg.InboundTag + ": " + err.Error(),
				)
				continue
			}
		} else {
			if cfg.ListenPort <= 0 {
				baselinePruningSafe = false
				s.appendLog(
					"wireguard interface resolve failed for " +
						cfg.InboundTag + ": missing listen port",
				)
				continue
			}

			if !allDumpLoaded {
				rawDump, err := wireGuardDumpAll(ctx)
				if err != nil {
					baselinePruningSafe = false
					s.appendLog(
						"wireguard all-interface stats query failed: " +
							err.Error(),
					)
					allDump = map[string]wireGuardInterfaceDump{}
					allDumpLoaded = true
					continue
				}

				allDump, err = parseWireGuardAllDump(string(rawDump))
				if err != nil {
					baselinePruningSafe = false
					s.appendLog(
						"wireguard all-interface stats parse failed: " +
							err.Error(),
					)
					allDump = map[string]wireGuardInterfaceDump{}
				}
				allDumpLoaded = true
			}

			matches := make([]wireGuardInterfaceDump, 0, 1)
			for _, item := range allDump {
				if item.ListenPort == cfg.ListenPort {
					matches = append(matches, item)
				}
			}
			if len(matches) != 1 {
				baselinePruningSafe = false
				s.appendLog(fmt.Sprintf(
					"wireguard interface resolve failed for %s: listen port %d matched %d interfaces",
					cfg.InboundTag,
					cfg.ListenPort,
					len(matches),
				))
				continue
			}

			interfaceName = matches[0].Name
			peers = matches[0].Peers
		}

		if err := s.reconcileWireGuardSessions(
			ctx,
			cfg,
			interfaceName,
			peers,
			observedAt,
		); err != nil {
			s.appendLog(
				"wireguard session reconcile failed for " +
					cfg.InboundTag + ": " + err.Error(),
			)
		}

		if wireGuardUsageAccountingEnabled(cfg) {
			for publicKey := range cfg.Peers {
				configuredKeys[wireGuardUsageBaselineKey(
					cfg.InboundTag,
					interfaceName,
					publicKey,
				)] = struct{}{}
			}
		}

		for _, peer := range peers {
			userID := cfg.Peers[strings.TrimSpace(peer.PublicKey)]
			if userID <= 0 {
				continue
			}
			if wireGuardHandshakeActive(
				peer.LatestHandshake,
				observedAt,
			) {
				onlineUsers[userID] = struct{}{}
			}
			if pendingAccounting != nil {
				continue
			}

			if !wireGuardUsageAccountingEnabled(cfg) {
				continue
			}

			total, err := wireGuardPeerTotalBytes(peer)
			if err != nil {
				continue
			}

			baselineKey := wireGuardUsageBaselineKey(
				cfg.InboundTag,
				interfaceName,
				peer.PublicKey,
			)
			baseline, exists := nextBaseline[baselineKey]
			if carry, ok := s.wireGuardUsageCarry[baselineKey]; ok {
				baseline = carry.NextBaseline
				exists = true
			}

			delta := total
			if exists && total >= baseline {
				delta = total - baseline
			}
			if exists && total < baseline {
				delta = total
			}

			nextBaseline[baselineKey] = total
			if delta == 0 {
				continue
			}

			key := aggregateKey{
				UserID:     userID,
				InboundTag: cfg.InboundTag,
			}
			sample := aggregated[key]
			sample.UserID = userID
			sample.InboundTag = cfg.InboundTag
			if ^uint64(0)-sample.Value >= delta {
				sample.Value += delta
			}
			aggregated[key] = sample
		}
	}

	if pendingAccounting != nil {
		return wireGuardUsageBatchProto(
			pendingAccounting,
			wireGuardSortedOnlineUserIDs(onlineUsers),
		), nil
	}

	if baselinePruningSafe {
		for key := range nextBaseline {
			if _, keep := configuredKeys[key]; !keep {
				delete(nextBaseline, key)
			}
		}
	}

	if len(aggregated) == 0 {
		if !wireGuardBaselinesEqual(
			s.wireGuardUsageBaseline,
			nextBaseline,
		) {
			previous := s.wireGuardUsageBaseline
			s.wireGuardUsageBaseline = nextBaseline
			if err := s.persistWireGuardUsageStateLocked(); err != nil {
				s.wireGuardUsageBaseline = previous
				return nil, err
			}
		}
		return wireGuardUsageBatchProto(nil, wireGuardSortedOnlineUserIDs(onlineUsers)), nil
	}

	keys := make([]aggregateKey, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].UserID == keys[j].UserID {
			return keys[i].InboundTag < keys[j].InboundTag
		}
		return keys[i].UserID < keys[j].UserID
	})

	samples := make([]wireGuardUsageSample, 0, len(keys))
	for _, key := range keys {
		samples = append(samples, aggregated[key])
	}

	pending := &wireGuardUsagePendingBatch{
		BatchID: fmt.Sprintf(
			"wireguard-%d",
			time.Now().UTC().UnixNano(),
		),
		Samples:      samples,
		NextBaseline: nextBaseline,
		CarryValues:  carryValues,
	}
	s.wireGuardUsagePending = pending

	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		s.wireGuardUsagePending = nil
		return nil, err
	}
	return wireGuardUsageBatchProto(pending, wireGuardSortedOnlineUserIDs(onlineUsers)), nil
}

func (s *Server) ackWireGuardUserUsage(
	ctx context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	return s.ackWireGuardUserUsageWithReflection(
		ctx,
		req,
		req.GetBatchId(),
	)
}

func (s *Server) ackWireGuardUserUsageWithReflection(
	_ context.Context,
	req *nodev1.AckUsageRequest,
	reflectionBatchID string,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	reflectionBatchID = strings.TrimSpace(reflectionBatchID)
	if reflectionBatchID == "" {
		reflectionBatchID = batchID
	}
	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()

	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.wireGuardUsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{Acknowledged: true}, nil
	}

	pending := s.wireGuardUsagePending
	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	previousBaseline := s.wireGuardUsageBaseline
	previousLastAcked := s.wireGuardUsageLastAckedBatchID
	previousAwaiting := append([]wireGuardUsageAwaitingReflectionBatch(nil), s.wireGuardUsageAwaitingReflection...)
	previousCarry := s.wireGuardUsageCarry
	nextCarry := cloneWireGuardUsageCarryMap(s.wireGuardUsageCarry)

	for key, consumed := range pending.CarryValues {
		if consumed == 0 {
			continue
		}

		current, ok := nextCarry[key]
		if !ok {
			return nil, fmt.Errorf(
				"wireguard ACK carry missing for key %q",
				key,
			)
		}
		if current.Value < consumed {
			return nil, fmt.Errorf(
				"wireguard ACK carry underflow for key %q: have %d, consumed %d",
				key,
				current.Value,
				consumed,
			)
		}

		if current.Value == consumed {
			delete(nextCarry, key)
			continue
		}

		current.Value -= consumed
		nextCarry[key] = current
	}

	s.wireGuardUsageBaseline = pending.NextBaseline
	s.wireGuardUsageCarry = nextCarry
	s.wireGuardUsageAwaitingReflection = append(
		s.wireGuardUsageAwaitingReflection,
		wireGuardUsageAwaitingReflectionBatch{
			BatchID: reflectionBatchID,
			Samples: append([]wireGuardUsageSample(nil), pending.Samples...),
		},
	)
	s.wireGuardUsagePending = nil
	s.wireGuardUsageLastAckedBatchID = batchID

	if err := s.persistWireGuardUsageStateLocked(); err != nil {
		s.wireGuardUsageBaseline = previousBaseline
		s.wireGuardUsageCarry = previousCarry
		s.wireGuardUsagePending = pending
		s.wireGuardUsageLastAckedBatchID = previousLastAcked
		s.wireGuardUsageAwaitingReflection = previousAwaiting
		return nil, err
	}

	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}

func wireGuardUsageBatchProto(
	pending *wireGuardUsagePendingBatch,
	onlineUserIDs []int64,
) *nodev1.UserUsageBatch {
	batchID := ""
	sampleCapacity := len(onlineUserIDs)
	if pending != nil {
		batchID = pending.BatchID
		sampleCapacity += len(pending.Samples)
	}

	stats := make(
		[]*nodev1.UserUsageSample,
		0,
		sampleCapacity,
	)

	if pending != nil {
		for _, sample := range pending.Samples {
			stats = append(stats, &nodev1.UserUsageSample{
				Uid:        "wireguard:" + strconv.FormatInt(sample.UserID, 10),
				Value:      sample.Value,
				InboundTag: sample.InboundTag,
			})
		}
	}

	for _, userID := range onlineUserIDs {
		stats = append(stats, &nodev1.UserUsageSample{
			Uid:   "online:wireguard:" + strconv.FormatInt(userID, 10),
			Value: 0,
		})
	}

	return &nodev1.UserUsageBatch{
		BatchId: batchID,
		Stats:   stats,
	}
}

func wireGuardSortedOnlineUserIDs(
	onlineUsers map[int64]struct{},
) []int64 {
	result := make([]int64, 0, len(onlineUsers))
	for userID := range onlineUsers {
		result = append(result, userID)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}

func wireGuardHandshakeActive(
	latestHandshake int64,
	now time.Time,
) bool {
	if latestHandshake <= 0 {
		return false
	}

	seen := time.Unix(latestHandshake, 0).UTC()
	cutoff := now.UTC().Add(-online.ActiveWindow)
	if seen.Before(cutoff) {
		return false
	}

	// Allow small clock skew, but reject clearly invalid future values.
	if seen.After(now.UTC().Add(30 * time.Second)) {
		return false
	}
	return true
}

func wireGuardBaselinesEqual(
	left map[string]uint64,
	right map[string]uint64,
) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
