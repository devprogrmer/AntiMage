package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// mergedUsagePendingBatch tracks a merged batch awaiting ACK
type mergedUsagePendingBatch struct {
	MergedBatchID  string
	OpenVPNBatchID string
	XrayBatchID    string
	CreatedAt      time.Time
}

// mergedUsageDiskState is the persisted merged state
type mergedUsageDiskState struct {
	Pending *mergedUsagePendingBatch `json:"pending,omitempty"`
}

func (s *Server) mergedUsageStatePath() string {
	return filepath.Join(
		s.cfg.DataDir,
		"merged",
		"usage-state.json",
	)
}

func (s *Server) ensureMergedUsageStateLoadedLocked() error {
	if s.mergedUsageLoaded {
		return nil
	}

	path := s.mergedUsageStatePath()

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mergedUsageLoaded = true
			return nil
		}

		return fmt.Errorf(
			"read merged usage state: %w",
			err,
		)
	}

	var state mergedUsageDiskState

	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf(
			"parse merged usage state: %w",
			err,
		)
	}

	s.mergedUsagePending = state.Pending
	s.mergedUsageLoaded = true

	return nil
}

func (s *Server) persistMergedUsageStateLocked() error {
	dir := filepath.Dir(s.mergedUsageStatePath())

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf(
			"create merged usage state directory: %w",
			err,
		)
	}

	state := mergedUsageDiskState{
		Pending: s.mergedUsagePending,
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf(
			"marshal merged usage state: %w",
			err,
		)
	}

	path := s.mergedUsageStatePath()
	tmp := path + ".tmp"

	file, err := os.OpenFile(
		tmp,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return fmt.Errorf(
			"open temporary merged usage state: %w",
			err,
		)
	}

	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}

	if _, err := file.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf(
			"write temporary merged usage state: %w",
			err,
		)
	}

	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf(
			"sync temporary merged usage state: %w",
			err,
		)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"close temporary merged usage state: %w",
			err,
		)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf(
			"replace merged usage state: %w",
			err,
		)
	}

	return nil
}

// mergeUserUsageBatches merges OpenVPN and Xray usage batches into a single batch
// Returns the SAME merged batch ID across retries until both children are ACKed
func (s *Server) mergeUserUsageBatches(ovpnBatch, xrayBatch *nodev1.UserUsageBatch) *nodev1.UserUsageBatch {
	s.mergedUsageMu.Lock()
	defer s.mergedUsageMu.Unlock()

	if err := s.ensureMergedUsageStateLoadedLocked(); err != nil {
		// Fallback to non-persistent merge on state load error
		return s.mergeUserUsageBatchesNoPersist(ovpnBatch, xrayBatch)
	}

	ovpnID := ovpnBatch.GetBatchId()
	xrayID := xrayBatch.GetBatchId()

	// If neither has a batch ID, return empty
	if ovpnID == "" && xrayID == "" {
		return &nodev1.UserUsageBatch{}
	}

	// If only one has a batch ID, return that one
	if ovpnID == "" {
		return xrayBatch
	}
	if xrayID == "" {
		return ovpnBatch
	}

	// Check if we have a pending merged batch for these exact children
	if s.mergedUsagePending != nil {
		if s.mergedUsagePending.OpenVPNBatchID == ovpnID &&
			s.mergedUsagePending.XrayBatchID == xrayID {
			// Return the SAME merged batch ID
			return s.buildMergedBatch(
				s.mergedUsagePending.MergedBatchID,
				ovpnBatch,
				xrayBatch,
			)
		}
	}

	// Create new merged batch
	mergedID := fmt.Sprintf("merged-%d", time.Now().UTC().UnixNano())

	s.mergedUsagePending = &mergedUsagePendingBatch{
		MergedBatchID:  mergedID,
		OpenVPNBatchID: ovpnID,
		XrayBatchID:    xrayID,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.persistMergedUsageStateLocked(); err != nil {
		// Fallback to non-persistent
		s.mergedUsagePending = nil
		return s.mergeUserUsageBatchesNoPersist(ovpnBatch, xrayBatch)
	}

	return s.buildMergedBatch(mergedID, ovpnBatch, xrayBatch)
}

func (s *Server) buildMergedBatch(
	mergedID string,
	ovpnBatch, xrayBatch *nodev1.UserUsageBatch,
) *nodev1.UserUsageBatch {
	mergedStats := make([]*nodev1.UserUsageSample, 0,
		len(ovpnBatch.GetStats())+len(xrayBatch.GetStats()))
	mergedStats = append(mergedStats, ovpnBatch.GetStats()...)
	mergedStats = append(mergedStats, xrayBatch.GetStats()...)

	mergedOnlineIps := make([]*nodev1.OnlineUserIP, 0,
		len(ovpnBatch.GetOnlineIps())+len(xrayBatch.GetOnlineIps()))
	mergedOnlineIps = append(mergedOnlineIps, ovpnBatch.GetOnlineIps()...)
	mergedOnlineIps = append(mergedOnlineIps, xrayBatch.GetOnlineIps()...)

	mergedSpeeds := make([]*nodev1.UserTrafficSpeed, 0,
		len(ovpnBatch.GetSpeeds())+len(xrayBatch.GetSpeeds()))
	mergedSpeeds = append(mergedSpeeds, ovpnBatch.GetSpeeds()...)
	mergedSpeeds = append(mergedSpeeds, xrayBatch.GetSpeeds()...)

	return &nodev1.UserUsageBatch{
		BatchId:   mergedID,
		Stats:     mergedStats,
		OnlineIps: mergedOnlineIps,
		Speeds:    mergedSpeeds,
	}
}

func (s *Server) mergeUserUsageBatchesNoPersist(ovpnBatch, xrayBatch *nodev1.UserUsageBatch) *nodev1.UserUsageBatch {
	ovpnID := ovpnBatch.GetBatchId()
	xrayID := xrayBatch.GetBatchId()

	if ovpnID == "" && xrayID == "" {
		return &nodev1.UserUsageBatch{}
	}

	if ovpnID == "" {
		return xrayBatch
	}
	if xrayID == "" {
		return ovpnBatch
	}

	mergedID := fmt.Sprintf("merged-%d", time.Now().UTC().UnixNano())
	return s.buildMergedBatch(mergedID, ovpnBatch, xrayBatch)
}

// ackMergedUserUsage handles ACK for merged batches
// ONLY succeeds when ALL child batches are successfully acknowledged
func (s *Server) ackMergedUserUsage(
	ctx context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := req.GetBatchId()

	s.mergedUsageMu.Lock()
	defer s.mergedUsageMu.Unlock()

	if err := s.ensureMergedUsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	pending := s.mergedUsagePending
	if pending == nil || pending.MergedBatchID != batchID {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	// ACK both children - MUST succeed for both
	var ovpnAcked, xrayAcked bool

	// ACK OpenVPN batch
	if pending.OpenVPNBatchID != "" {
		resp, err := s.ackOpenVPNUserUsage(ctx, &nodev1.AckUsageRequest{
			BatchId: pending.OpenVPNBatchID,
		})
		if err != nil {
			return nil, fmt.Errorf("openvpn ack failed: %w", err)
		}
		ovpnAcked = resp.GetAcknowledged()
	} else {
		ovpnAcked = true // No OpenVPN batch to ACK
	}

	// ACK Xray batch
	if pending.XrayBatchID != "" {
		resp, err := s.ackXrayUserUsage(ctx, &nodev1.AckUsageRequest{
			BatchId: pending.XrayBatchID,
		})
		if err != nil {
			return nil, fmt.Errorf("xray ack failed: %w", err)
		}
		xrayAcked = resp.GetAcknowledged()
	} else {
		xrayAcked = true // No Xray batch to ACK
	}

	// BOTH must succeed
	if !ovpnAcked || !xrayAcked {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	// Clear pending merged batch
	s.mergedUsagePending = nil

	if err := s.persistMergedUsageStateLocked(); err != nil {
		return nil, err
	}

	return &nodev1.AckUsageResponse{Acknowledged: true}, nil
}
