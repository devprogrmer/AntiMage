package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type combinedUsagePendingBatch struct {
	BatchID          string    `json:"batch_id"`
	CoreBatchID      string    `json:"core_batch_id"`
	WireGuardBatchID string    `json:"wireguard_batch_id"`
	CreatedAt        time.Time `json:"created_at"`
}

type combinedUsageDiskState struct {
	Pending          *combinedUsagePendingBatch `json:"pending,omitempty"`
	LastAckedBatchID string                     `json:"last_acked_batch_id,omitempty"`
}

func (s *Server) combinedUsageStatePath() string {
	return filepath.Join(s.cfg.DataDir, "combined", "usage-state.json")
}

func (s *Server) ensureCombinedUsageStateLoadedLocked() error {
	if s.combinedUsageLoaded {
		return nil
	}
	raw, err := os.ReadFile(s.combinedUsageStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			s.combinedUsageLoaded = true
			return nil
		}
		return fmt.Errorf("read combined usage state: %w", err)
	}

	var state combinedUsageDiskState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse combined usage state: %w", err)
	}
	s.combinedUsagePending = state.Pending
	s.combinedUsageLastAckedBatchID = state.LastAckedBatchID
	s.combinedUsageLoaded = true
	return nil
}

func (s *Server) persistCombinedUsageStateLocked() error {
	dir := filepath.Dir(s.combinedUsageStatePath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create combined usage state directory: %w", err)
	}

	raw, err := json.Marshal(combinedUsageDiskState{
		Pending:          s.combinedUsagePending,
		LastAckedBatchID: s.combinedUsageLastAckedBatchID,
	})
	if err != nil {
		return fmt.Errorf("marshal combined usage state: %w", err)
	}

	path := s.combinedUsageStatePath()
	tmp := path + ".tmp"
	file, err := os.OpenFile(
		tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return fmt.Errorf("open temporary combined usage state: %w", err)
	}

	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}
	if _, err := file.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("write temporary combined usage state: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary combined usage state: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temporary combined usage state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace combined usage state: %w", err)
	}
	return nil
}

func (s *Server) combineUserUsageBatches(
	coreBatch *nodev1.UserUsageBatch,
	wireGuardBatch *nodev1.UserUsageBatch,
) (*nodev1.UserUsageBatch, error) {
	coreID := ""
	if coreBatch != nil {
		coreID = strings.TrimSpace(coreBatch.GetBatchId())
	}
	wgID := ""
	if wireGuardBatch != nil {
		wgID = strings.TrimSpace(wireGuardBatch.GetBatchId())
	}

	if coreID == "" && wgID == "" {
		return &nodev1.UserUsageBatch{}, nil
	}
	if coreID == "" {
		return wireGuardBatch, nil
	}
	if wgID == "" {
		return coreBatch, nil
	}

	s.combinedUsageMu.Lock()
	defer s.combinedUsageMu.Unlock()

	if err := s.ensureCombinedUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if pending := s.combinedUsagePending; pending != nil {
		if pending.CoreBatchID != coreID ||
			pending.WireGuardBatchID != wgID {
			return nil, fmt.Errorf(
				"combined usage child batch changed before ACK",
			)
		}
		return buildCombinedUsageBatch(
			pending.BatchID,
			coreBatch,
			wireGuardBatch,
		), nil
	}

	pending := &combinedUsagePendingBatch{
		BatchID: fmt.Sprintf(
			"combined-%d",
			time.Now().UTC().UnixNano(),
		),
		CoreBatchID:      coreID,
		WireGuardBatchID: wgID,
		CreatedAt:        time.Now().UTC(),
	}
	s.combinedUsagePending = pending
	if err := s.persistCombinedUsageStateLocked(); err != nil {
		s.combinedUsagePending = nil
		return nil, err
	}
	return buildCombinedUsageBatch(
		pending.BatchID,
		coreBatch,
		wireGuardBatch,
	), nil
}

func buildCombinedUsageBatch(
	batchID string,
	coreBatch *nodev1.UserUsageBatch,
	wireGuardBatch *nodev1.UserUsageBatch,
) *nodev1.UserUsageBatch {
	stats := make([]*nodev1.UserUsageSample, 0,
		len(coreBatch.GetStats())+len(wireGuardBatch.GetStats()))
	stats = append(stats, coreBatch.GetStats()...)
	stats = append(stats, wireGuardBatch.GetStats()...)

	onlineIPs := make([]*nodev1.OnlineUserIP, 0,
		len(coreBatch.GetOnlineIps())+len(wireGuardBatch.GetOnlineIps()))
	onlineIPs = append(onlineIPs, coreBatch.GetOnlineIps()...)
	onlineIPs = append(onlineIPs, wireGuardBatch.GetOnlineIps()...)

	speeds := make([]*nodev1.UserTrafficSpeed, 0,
		len(coreBatch.GetSpeeds())+len(wireGuardBatch.GetSpeeds()))
	speeds = append(speeds, coreBatch.GetSpeeds()...)
	speeds = append(speeds, wireGuardBatch.GetSpeeds()...)

	return &nodev1.UserUsageBatch{
		BatchId:   batchID,
		Stats:     stats,
		OnlineIps: onlineIPs,
		Speeds:    speeds,
	}
}

func (s *Server) ackCombinedUserUsage(
	ctx context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())

	s.combinedUsageMu.Lock()
	defer s.combinedUsageMu.Unlock()

	if err := s.ensureCombinedUsageStateLoadedLocked(); err != nil {
		return nil, err
	}
	if s.combinedUsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{Acknowledged: true}, nil
	}

	pending := s.combinedUsagePending
	if pending == nil || pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	coreAck, err := s.ackUsageChildBatch(ctx, pending.CoreBatchID)
	if err != nil {
		return nil, fmt.Errorf("combined core ACK failed: %w", err)
	}
	wgResp, err := s.ackWireGuardUserUsageWithReflection(
		ctx,
		&nodev1.AckUsageRequest{
			BatchId: pending.WireGuardBatchID,
		},
		batchID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"combined wireguard ACK failed: %w",
			err,
		)
	}
	wgAck := wgResp.GetAcknowledged()
	if !coreAck || !wgAck {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	previousPending := s.combinedUsagePending
	previousLastAcked := s.combinedUsageLastAckedBatchID
	s.combinedUsagePending = nil
	s.combinedUsageLastAckedBatchID = batchID

	if err := s.persistCombinedUsageStateLocked(); err != nil {
		s.combinedUsagePending = previousPending
		s.combinedUsageLastAckedBatchID = previousLastAcked
		return nil, err
	}
	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}

func (s *Server) ackUsageChildBatch(
	ctx context.Context,
	batchID string,
) (bool, error) {
	req := &nodev1.AckUsageRequest{BatchId: batchID}
	switch {
	case strings.HasPrefix(batchID, "openvpn-"):
		resp, err := s.ackOpenVPNUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "xray-"):
		resp, err := s.ackXrayUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "merged-"):
		resp, err := s.ackMergedUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "wireguard-"):
		resp, err := s.ackWireGuardUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	default:
		return false, nil
	}
}
