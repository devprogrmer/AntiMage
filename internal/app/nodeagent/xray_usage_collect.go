package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// xrayStatsClient queries Xray's local stats API via xray api statsquery command
type xrayStatsClient struct {
	xrayPath string
	apiPort  int
}

func newXrayStatsClient(xrayPath string, apiPort int) *xrayStatsClient {
	return &xrayStatsClient{
		xrayPath: xrayPath,
		apiPort:  apiPort,
	}
}

// queryStats queries Xray stats via 'xray api statsquery' command
// Returns structured stats parsed from JSON output
func (c *xrayStatsClient) queryStats(ctx context.Context, pattern string, reset bool) ([]xrayStat, error) {
	// Build command: xray api statsquery --server=127.0.0.1:apiPort [--pattern=...] [--reset]
	args := []string{
		"api", "statsquery",
		fmt.Sprintf("--server=127.0.0.1:%d", c.apiPort),
	}

	if pattern != "" {
		args = append(args, fmt.Sprintf("--pattern=%s", pattern))
	}

	if reset {
		args = append(args, "--reset")
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("xray api statsquery command failed: %w", err)
	}

	// Parse JSON output - reuse existing xrayStatsQueryResponse from route_stats.go
	var response xrayStatsQueryResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("parse xray stats JSON: %w", err)
	}

	stats := make([]xrayStat, 0, len(response.Stat))
	for _, s := range response.Stat {
		value, err := parseXrayStatValue(s.Value)
		if err != nil {
			continue
		}
		stats = append(stats, xrayStat{
			Name:  s.Name,
			Value: value,
		})
	}

	return stats, nil
}

type xrayStat struct {
	Name  string
	Value int64
}

func (s *Server) collectXrayUserUsage(
	ctx context.Context,
	_ *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()

	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	// If there's a pending batch, return it (waiting for ACK)
	if s.xrayUsagePending != nil {
		return xrayUsageBatchProto(s.xrayUsagePending), nil
	}

	// Check if Xray runtime is actually running
	s.mu.Lock()
	xrayRunning := s.lastRuntime != nil
	xrayPath := s.cfg.XrayPath
	xrayAPIPort := s.cfg.XrayAPIPort
	s.mu.Unlock()

	if !xrayRunning {
		return &nodev1.UserUsageBatch{}, nil
	}

	// Query Xray stats via CLI with user pattern
	client := newXrayStatsClient(xrayPath, xrayAPIPort)

	stats, err := client.queryStats(ctx, "user>>>", false)
	if err != nil {
		s.appendLog("xray user stats query failed: " + err.Error())
		return &nodev1.UserUsageBatch{}, nil
	}

	if s.xrayUsageBaseline == nil {
		s.xrayUsageBaseline = make(map[string]uint64)
	}

	nextBaseline := make(
		map[string]uint64,
		len(s.xrayUsageBaseline),
	)
	for key, value := range s.xrayUsageBaseline {
		nextBaseline[key] = value
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(map[aggregateKey]xrayUsageSample)

	for _, stat := range stats {
		statName, ok := parseXrayStatName(stat.Name)
		if !ok || statName.Type != "user" {
			continue
		}

		identity, err := parseXrayUserEmail(statName.Email)
		if err != nil {
			s.appendLog("xray user email parse failed for " + statName.Email + ": " + err.Error())
			continue
		}

		if identity.UserID <= 0 {
			continue
		}

		// Build a session key for baseline tracking
		// Format: email:direction
		sessionKey := statName.Email + ":" + statName.Direction

		baseline, exists := s.xrayUsageBaseline[sessionKey]

		// Convert to uint64 for safe arithmetic
		currentValue := uint64(0)
		if stat.Value >= 0 {
			currentValue = uint64(stat.Value)
		}

		delta := currentValue

		if exists && currentValue >= baseline {
			delta = currentValue - baseline
		}

		// Counter reset detection: if current < baseline, treat as fresh start
		if exists && currentValue < baseline {
			delta = currentValue
		}

		nextBaseline[sessionKey] = currentValue

		key := aggregateKey{
			UserID:     identity.UserID,
			InboundTag: identity.InboundTag,
		}

		sample := aggregated[key]
		sample.UserID = identity.UserID
		sample.InboundTag = identity.InboundTag
		sample.Online = true

		// Accumulate delta with overflow protection
		if ^uint64(0)-sample.Value >= delta {
			sample.Value += delta
		}

		aggregated[key] = sample
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
		[]xrayUsageSample,
		0,
		len(keys),
	)

	for _, key := range keys {
		samples = append(samples, aggregated[key])
	}

	pending := &xrayUsagePendingBatch{
		BatchID: fmt.Sprintf(
			"xray-%d",
			time.Now().UTC().UnixNano(),
		),
		Samples:      samples,
		NextBaseline: nextBaseline,
	}

	s.xrayUsagePending = pending

	if err := s.persistXrayUsageStateLocked(); err != nil {
		s.xrayUsagePending = nil
		return nil, err
	}

	return xrayUsageBatchProto(pending), nil
}

func (s *Server) ackXrayUserUsage(
	_ context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	s.xrayUsageMu.Lock()
	defer s.xrayUsageMu.Unlock()

	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	// Idempotent ACK: If this batch was already successfully ACKed, return true
	if s.xrayUsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: true,
		}, nil
	}

	pending := s.xrayUsagePending

	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	previousBaseline := s.xrayUsageBaseline
	previousLastAcked := s.xrayUsageLastAckedBatchID

	s.xrayUsageBaseline = pending.NextBaseline
	s.xrayUsagePending = nil
	s.xrayUsageLastAckedBatchID = batchID

	if err := s.persistXrayUsageStateLocked(); err != nil {
		s.xrayUsageBaseline = previousBaseline
		s.xrayUsagePending = pending
		s.xrayUsageLastAckedBatchID = previousLastAcked
		return nil, err
	}

	return &nodev1.AckUsageResponse{
		Acknowledged: true,
	}, nil
}

func xrayUsageBatchProto(
	pending *xrayUsagePendingBatch,
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
		uid := "xray:" +
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
