package nodeagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// mergeUserUsageBatches merges OpenVPN and Xray usage batches into a single batch
func (s *Server) mergeUserUsageBatches(ovpnBatch, xrayBatch *nodev1.UserUsageBatch) *nodev1.UserUsageBatch {
	ovpnID := strings.TrimSpace(ovpnBatch.GetBatchId())
	xrayID := strings.TrimSpace(xrayBatch.GetBatchId())

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

	// Both have batch IDs - merge them
	mergedStats := make([]*nodev1.UserUsageSample, 0, len(ovpnBatch.GetStats())+len(xrayBatch.GetStats()))
	mergedStats = append(mergedStats, ovpnBatch.GetStats()...)
	mergedStats = append(mergedStats, xrayBatch.GetStats()...)

	mergedOnlineIps := make([]*nodev1.OnlineUserIP, 0, len(ovpnBatch.GetOnlineIps())+len(xrayBatch.GetOnlineIps()))
	mergedOnlineIps = append(mergedOnlineIps, ovpnBatch.GetOnlineIps()...)
	mergedOnlineIps = append(mergedOnlineIps, xrayBatch.GetOnlineIps()...)

	mergedSpeeds := make([]*nodev1.UserTrafficSpeed, 0, len(ovpnBatch.GetSpeeds())+len(xrayBatch.GetSpeeds()))
	mergedSpeeds = append(mergedSpeeds, ovpnBatch.GetSpeeds()...)
	mergedSpeeds = append(mergedSpeeds, xrayBatch.GetSpeeds()...)

	return &nodev1.UserUsageBatch{
		BatchId:   fmt.Sprintf("merged-%d-%s-%s", time.Now().UTC().UnixNano(), ovpnID, xrayID),
		Stats:     mergedStats,
		OnlineIps: mergedOnlineIps,
		Speeds:    mergedSpeeds,
	}
}

// ackMergedUserUsage handles ACK for merged batches
func (s *Server) ackMergedUserUsage(
	ctx context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if !strings.HasPrefix(batchID, "merged-") {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	// Extract the original batch IDs from the merged ID
	// Format: merged-<timestamp>-<ovpnID>-<xrayID>
	parts := strings.SplitN(batchID, "-", 4)
	if len(parts) < 4 {
		return &nodev1.AckUsageResponse{Acknowledged: false}, nil
	}

	ovpnID := parts[2]
	xrayID := parts[3]

	ovpnAcked := false
	xrayAcked := false

	// ACK OpenVPN batch
	if ovpnID != "" && strings.HasPrefix(ovpnID, "openvpn") {
		resp, err := s.ackOpenVPNUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: ovpnID})
		if err == nil && resp.GetAcknowledged() {
			ovpnAcked = true
		}
	}

	// ACK Xray batch
	if xrayID != "" && strings.HasPrefix(xrayID, "xray") {
		resp, err := s.ackXrayUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: xrayID})
		if err == nil && resp.GetAcknowledged() {
			xrayAcked = true
		}
	}

	// Consider the merged batch acknowledged if at least one component was acknowledged
	acknowledged := ovpnAcked || xrayAcked

	return &nodev1.AckUsageResponse{
		Acknowledged: acknowledged,
	}, nil
}
