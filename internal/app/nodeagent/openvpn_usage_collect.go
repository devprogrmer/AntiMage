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

type openVPNUsageRuntimeConfig struct {
	InboundTag string           `json:"inbound_tag"`
	StatusFile string           `json:"status_file"`
	Users      map[string]int64 `json:"users"`
}

type openVPNUsageSample struct {
	UserID     int64
	InboundTag string
	Value      uint64
	Online     bool
}

type openVPNUsagePendingBatch struct {
	BatchID      string
	Samples      []openVPNUsageSample
	NextBaseline map[string]uint64
}

func (s *Server) collectOpenVPNUserUsage(
	ctx context.Context,
	_ *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.openVPNUsageMu.Lock()
	defer s.openVPNUsageMu.Unlock()

	if err := s.ensureOpenVPNUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	if s.openVPNUsageBaseline == nil {
		s.openVPNUsageBaseline = make(map[string]uint64)
	}

	nextBaseline := make(
		map[string]uint64,
		len(s.openVPNUsageBaseline),
	)
	for key, value := range s.openVPNUsageBaseline {
		nextBaseline[key] = value
	}

	// Seed legacy pending native snapshots: their deltas are already in the batch.
	if pending := s.openVPNUsagePending; pending != nil {
		legacy := true
		for key := range pending.NextBaseline {
			if _, _, ok := offlineAccountingOwner(key); ok {
				legacy = false
				break
			}
		}
		if legacy {
			values := make([]offlinePendingSample, 0, len(pending.Samples))
			for _, sample := range pending.Samples {
				values = append(values, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
			}
			if err := offlineSeedLegacyPending(nextBaseline, pending.BatchID, values); err != nil {
				return nil, err
			}
			for key, value := range pending.NextBaseline {
				if nextBaseline[key] < value {
					nextBaseline[key] = value
				}
			}
		}
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]openVPNUsageSample)
	for key, total := range nextBaseline {
		uid, tag, ok := offlineAccountingOwner(key)
		if !ok {
			continue
		}
		sent := nextBaseline[offlineAccountingSentKey(key)]
		if sent > total {
			return nil, fmt.Errorf("invalid offline accounting baseline")
		}
		if total > sent {
			aggregated[aggregateKey{uid, tag}] = openVPNUsageSample{UserID: uid, InboundTag: tag, Value: total - sent}
		}
	}
	scannedInboundTags := make(map[string]struct{})
	activeSessionKeys := make(map[string]struct{})

	roots, err := offlineHelperRoots(s.cfg.DataDir, "openvpn")
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		tag := filepath.Base(root)

		usageConfigPath := filepath.Join(
			root,
			"usage-helper.json",
		)

		rawConfig, err := os.ReadFile(usageConfigPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.appendLog(
				"openvpn usage config read failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		var cfg openVPNUsageRuntimeConfig
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			s.appendLog(
				"openvpn usage config parse failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		if strings.TrimSpace(cfg.InboundTag) == "" {
			cfg.InboundTag = tag
		}

		statusPath := strings.TrimSpace(cfg.StatusFile)
		if statusPath == "" {
			statusPath = filepath.Join(root, "status.tsv")
		}

		rawStatus, err := openVPNOfflineReadStatus(statusPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.appendLog(
				"openvpn status read failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		clients, err := parseOpenVPNStatusV3(
			string(rawStatus),
		)
		if err != nil {
			s.appendLog(
				"openvpn status parse failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}
		if preview := offlinePreview(ctx); preview != nil {
			if preview.OpenVPNClients == nil {
				preview.OpenVPNClients = make(map[string][]openVPNStatusClient)
			}
			preview.OpenVPNClients[root] = clients
		}

		scannedInboundTags[cfg.InboundTag] = struct{}{}

		for _, client := range clients {
			activeSessionKeys[openVPNStatusSessionKey(cfg.InboundTag, client)] = struct{}{}
			username := strings.TrimSpace(client.Username)

			userID := cfg.Users[username]

			if userID <= 0 {
				commonName := strings.TrimSpace(
					client.CommonName,
				)
				userID = cfg.Users[commonName]
			}

			if userID <= 0 {
				continue
			}

			total, err := openVPNStatusTotalBytes(client)
			if err != nil {
				continue
			}

			generation, err := s.openVPNOfflineGeneration(cfg.InboundTag, root, offlinePreview(ctx) == nil)
			if err != nil {
				return nil, err
			}
			if generation == "" && strings.TrimSpace(client.ConnectedSince) == "" {
				return nil, fmt.Errorf("OpenVPN accounting requires daemon generation or connected-since identity")
			}
			sessionKey := openVPNStatusSessionKey(cfg.InboundTag, client) + "\x00since\x00" + client.ConnectedSince + "\x00generation\x00" + generation

			baseline, exists :=
				nextBaseline[sessionKey]

			if !exists {
				legacyKey := openVPNStatusSessionKey(cfg.InboundTag, client)
				baseline, exists = nextBaseline[legacyKey]
				delete(nextBaseline, legacyKey)
			}

			delta := total

			if exists && total >= baseline {
				delta = total - baseline
			}

			// A lower counter means the OpenVPN session/process
			// restarted. Treat the new counter as a fresh baseline.
			if exists && total < baseline {
				delta = total
			}

			nextBaseline[sessionKey] = total

			key := aggregateKey{
				UserID:     userID,
				InboundTag: cfg.InboundTag,
			}

			sample := aggregated[key]
			sample.UserID = userID
			sample.InboundTag = cfg.InboundTag
			sample.Online = true

			owner := offlineAccountingTotalKey(userID, cfg.InboundTag)
			if ^uint64(0)-sample.Value < delta || ^uint64(0)-nextBaseline[owner] < delta {
				return nil, fmt.Errorf("offline accounting overflow")
			}
			sample.Value += delta
			nextBaseline[owner] += delta

			aggregated[key] = sample
		}
	}

	if len(nextBaseline) > maxAccountingCounterSeries {
		return nil, fmt.Errorf("offline accounting capacity exceeded; refusing to discard usage")
	}
	if preview := offlinePreview(ctx); preview != nil {
		preview.Baseline = nextBaseline
		if pending := s.openVPNUsagePending; pending != nil {
			preview.PendingID = pending.BatchID
			for _, sample := range pending.Samples {
				preview.PendingSamples = append(preview.PendingSamples, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
			}
		}
		return &nodev1.UserUsageBatch{}, nil
	}
	previousSampled := s.openVPNUsageBaseline
	s.openVPNUsageBaseline = nextBaseline
	if err := s.persistOpenVPNUsageStateLocked(); err != nil {
		s.openVPNUsageBaseline = previousSampled
		return nil, err
	}
	if s.openVPNUsagePending != nil {
		return openVPNUsageBatchProto(s.openVPNUsagePending), nil
	}
	if offlineCheckpointOnly(ctx) {
		return &nodev1.UserUsageBatch{}, nil
	}

	if len(aggregated) == 0 {
		return &nodev1.UserUsageBatch{}, nil
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

	samples := make(
		[]openVPNUsageSample,
		0,
		len(keys),
	)

	for _, key := range keys {
		samples = append(samples, aggregated[key])
	}

	pending := &openVPNUsagePendingBatch{
		BatchID: fmt.Sprintf(
			"openvpn-%d",
			time.Now().UTC().UnixNano(),
		),
		Samples:      samples,
		NextBaseline: nextBaseline,
	}

	s.openVPNUsagePending = pending

	if err := s.persistOpenVPNUsageStateLocked(); err != nil {
		s.openVPNUsagePending = nil
		return nil, err
	}

	return openVPNUsageBatchProto(pending), nil
}

func (s *Server) ackOpenVPNUserUsage(
	_ context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	s.openVPNUsageMu.Lock()
	defer s.openVPNUsageMu.Unlock()

	if err := s.ensureOpenVPNUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	// Idempotent ACK: If this batch was already successfully ACKed, return true
	if s.openVPNUsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: true,
		}, nil
	}

	pending := s.openVPNUsagePending

	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	previousBaseline := s.openVPNUsageBaseline
	previousLastAcked := s.openVPNUsageLastAckedBatchID

	values := make([]offlinePendingSample, 0, len(pending.Samples))
	for _, sample := range pending.Samples {
		values = append(values, offlinePendingSample{sample.UserID, sample.InboundTag, sample.Value})
	}
	ackBaseline, err := offlineAckSnapshot(s.openVPNUsageBaseline, pending.NextBaseline, batchID, values)
	if err != nil {
		return nil, err
	}
	s.openVPNUsageBaseline = ackBaseline
	s.openVPNUsagePending = nil
	s.openVPNUsageLastAckedBatchID = batchID

	if err := s.persistOpenVPNUsageStateLocked(); err != nil {
		s.openVPNUsageBaseline = previousBaseline
		s.openVPNUsagePending = pending
		s.openVPNUsageLastAckedBatchID = previousLastAcked
		return nil, err
	}

	return &nodev1.AckUsageResponse{
		Acknowledged: true,
	}, nil
}

func openVPNUsageBatchProto(
	pending *openVPNUsagePendingBatch,
) *nodev1.UserUsageBatch {
	if pending == nil {
		return &nodev1.UserUsageBatch{}
	}

	stats := make(
		[]*nodev1.UserUsageSample,
		0,
		len(pending.Samples),
	)

	for _, sample := range pending.Samples {
		uid := "openvpn:" +
			strconv.FormatInt(sample.UserID, 10)

		if sample.Value == 0 && sample.Online {
			uid = "online:" + uid
		}

		stats = append(
			stats,
			&nodev1.UserUsageSample{
				Uid:        uid,
				Value:      sample.Value,
				InboundTag: sample.InboundTag,
			},
		)
	}

	return &nodev1.UserUsageBatch{
		BatchId: pending.BatchID,
		Stats:   stats,
	}
}

func (s *Server) activeOpenVPNRuntimeTags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	tags := make([]string, 0, len(s.openVPNRuntimes))

	for tag := range s.openVPNRuntimes {
		tags = append(tags, tag)
	}

	sort.Strings(tags)
	return tags
}
