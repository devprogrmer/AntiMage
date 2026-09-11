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

	if s.openVPNUsagePending != nil {
		return openVPNUsageBatchProto(s.openVPNUsagePending), nil
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

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]openVPNUsageSample)
	scannedInboundTags := make(map[string]struct{})
	activeSessionKeys := make(map[string]struct{})

	for _, tag := range s.activeOpenVPNRuntimeTags() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		root := filepath.Join(
			s.cfg.DataDir,
			"openvpn",
			openVPNRuntimeDirName(tag),
		)

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

		rawStatus, err := os.ReadFile(statusPath)
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

			sessionKey := openVPNStatusSessionKey(
				cfg.InboundTag,
				client,
			)

			baseline, exists :=
				s.openVPNUsageBaseline[sessionKey]

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

			if ^uint64(0)-sample.Value >= delta {
				sample.Value += delta
			}

			aggregated[key] = sample
		}
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

	s.openVPNUsageBaseline = pending.NextBaseline
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
