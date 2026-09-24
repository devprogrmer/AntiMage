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
	BatchID           string    `json:"batch_id"`
	CoreBatchID       string    `json:"core_batch_id"`
	WireGuardBatchID  string    `json:"wireguard_batch_id"`
	L2TPBatchID       string    `json:"l2tp_batch_id,omitempty"`
	PPTPBatchID       string    `json:"pptp_batch_id,omitempty"`
	AmneziaWGBatchID  string    `json:"amneziawg_batch_id,omitempty"`
	IKEv2BatchID      string    `json:"ikev2_batch_id,omitempty"`
	AnyConnectBatchID string    `json:"anyconnect_batch_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
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
	optionalNative ...*nodev1.UserUsageBatch,
) (*nodev1.UserUsageBatch, error) {
	var l2tpBatch *nodev1.UserUsageBatch
	var pptpBatch *nodev1.UserUsageBatch
	var awgBatch *nodev1.UserUsageBatch
	var ikev2Batch *nodev1.UserUsageBatch
	var anyConnectBatch *nodev1.UserUsageBatch
	if len(optionalNative) > 0 {
		l2tpBatch = optionalNative[0]
	}
	if len(optionalNative) > 1 {
		pptpBatch = optionalNative[1]
	}
	if len(optionalNative) > 2 {
		awgBatch = optionalNative[2]
	}
	if len(optionalNative) > 3 {
		ikev2Batch = optionalNative[3]
	}
	if len(optionalNative) > 4 {
		anyConnectBatch = optionalNative[4]
	}
	coreID := ""
	if coreBatch != nil {
		coreID = strings.TrimSpace(coreBatch.GetBatchId())
	}
	wgID := ""
	if wireGuardBatch != nil {
		wgID = strings.TrimSpace(wireGuardBatch.GetBatchId())
	}
	l2tpID := ""
	if l2tpBatch != nil {
		l2tpID = strings.TrimSpace(l2tpBatch.GetBatchId())
	}
	pptpID := ""
	if pptpBatch != nil {
		pptpID = strings.TrimSpace(pptpBatch.GetBatchId())
	}
	awgID := ""
	if awgBatch != nil {
		awgID = strings.TrimSpace(awgBatch.GetBatchId())
	}
	ikev2ID := ""
	if ikev2Batch != nil {
		ikev2ID = strings.TrimSpace(ikev2Batch.GetBatchId())
	}
	anyConnectID := ""
	if anyConnectBatch != nil {
		anyConnectID = strings.TrimSpace(anyConnectBatch.GetBatchId())
	}

	nonEmpty := 0
	if coreID != "" {
		nonEmpty++
	}
	if wgID != "" {
		nonEmpty++
	}
	if l2tpID != "" {
		nonEmpty++
	}
	if pptpID != "" {
		nonEmpty++
	}
	if awgID != "" {
		nonEmpty++
	}
	if ikev2ID != "" {
		nonEmpty++
	}
	if anyConnectID != "" {
		nonEmpty++
	}
	if nonEmpty == 0 {
		return &nodev1.UserUsageBatch{}, nil
	}
	if nonEmpty == 1 {
		if coreID != "" {
			return coreBatch, nil
		}
		if wgID != "" {
			return wireGuardBatch, nil
		}
		if l2tpID != "" {
			return l2tpBatch, nil
		}
		if awgID != "" {
			return awgBatch, nil
		}
		if ikev2ID != "" {
			return ikev2Batch, nil
		}
		if anyConnectID != "" {
			return anyConnectBatch, nil
		}
		return pptpBatch, nil
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

		// Backward compatibility with a state written before L2TP
		// was included in the combined usage batch.
		if pending.L2TPBatchID == "" {
			l2tpBatch = nil
		} else if pending.L2TPBatchID != l2tpID {
			return nil, fmt.Errorf(
				"combined L2TP child batch changed before ACK",
			)
		}

		if pending.PPTPBatchID == "" {
			pptpBatch = nil
		} else if pending.PPTPBatchID != pptpID {
			return nil, fmt.Errorf(
				"combined PPTP child batch changed before ACK",
			)
		}
		if pending.AmneziaWGBatchID == "" {
			awgBatch = nil
		} else if pending.AmneziaWGBatchID != awgID {
			return nil, fmt.Errorf("combined AmneziaWG child batch changed before ACK")
		}

		if pending.IKEv2BatchID == "" {
			ikev2Batch = nil
		} else if pending.IKEv2BatchID != ikev2ID {
			return nil, fmt.Errorf(
				"combined IKEv2 child batch changed before ACK",
			)
		}
		if pending.AnyConnectBatchID == "" {
			anyConnectBatch = nil
		} else if pending.AnyConnectBatchID != anyConnectID {
			return nil, fmt.Errorf("combined AnyConnect child batch changed before ACK")
		}

		return buildCombinedUsageBatch(
			pending.BatchID,
			coreBatch,
			wireGuardBatch,
			l2tpBatch,
			pptpBatch,
			awgBatch,
			ikev2Batch,
			anyConnectBatch,
		), nil
	}

	pending := &combinedUsagePendingBatch{
		BatchID: fmt.Sprintf(
			"combined-%d",
			time.Now().UTC().UnixNano(),
		),
		CoreBatchID:       coreID,
		WireGuardBatchID:  wgID,
		L2TPBatchID:       l2tpID,
		PPTPBatchID:       pptpID,
		AmneziaWGBatchID:  awgID,
		IKEv2BatchID:      ikev2ID,
		AnyConnectBatchID: anyConnectID,
		CreatedAt:         time.Now().UTC(),
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
		l2tpBatch,
		pptpBatch,
		awgBatch,
		ikev2Batch,
		anyConnectBatch,
	), nil
}

func buildCombinedUsageBatch(
	batchID string,
	coreBatch *nodev1.UserUsageBatch,
	wireGuardBatch *nodev1.UserUsageBatch,
	optionalNative ...*nodev1.UserUsageBatch,
) *nodev1.UserUsageBatch {
	var l2tpBatch *nodev1.UserUsageBatch
	var pptpBatch *nodev1.UserUsageBatch
	var awgBatch *nodev1.UserUsageBatch
	var ikev2Batch *nodev1.UserUsageBatch
	var anyConnectBatch *nodev1.UserUsageBatch
	if len(optionalNative) > 0 {
		l2tpBatch = optionalNative[0]
	}
	if len(optionalNative) > 1 {
		pptpBatch = optionalNative[1]
	}
	if len(optionalNative) > 2 {
		awgBatch = optionalNative[2]
	}
	if len(optionalNative) > 3 {
		ikev2Batch = optionalNative[3]
	}
	if len(optionalNative) > 4 {
		anyConnectBatch = optionalNative[4]
	}
	stats := make([]*nodev1.UserUsageSample, 0,
		len(coreBatch.GetStats())+
			len(wireGuardBatch.GetStats())+
			len(l2tpBatch.GetStats())+
			len(pptpBatch.GetStats()))
	stats = append(stats, awgBatch.GetStats()...)
	stats = append(stats, coreBatch.GetStats()...)
	stats = append(stats, wireGuardBatch.GetStats()...)
	stats = append(stats, l2tpBatch.GetStats()...)
	stats = append(stats, pptpBatch.GetStats()...)
	stats = append(stats, ikev2Batch.GetStats()...)
	stats = append(stats, anyConnectBatch.GetStats()...)

	onlineIPs := make([]*nodev1.OnlineUserIP, 0,
		len(coreBatch.GetOnlineIps())+
			len(wireGuardBatch.GetOnlineIps())+
			len(l2tpBatch.GetOnlineIps())+
			len(pptpBatch.GetOnlineIps()))
	onlineIPs = append(onlineIPs, awgBatch.GetOnlineIps()...)
	onlineIPs = append(onlineIPs, coreBatch.GetOnlineIps()...)
	onlineIPs = append(onlineIPs, wireGuardBatch.GetOnlineIps()...)
	onlineIPs = append(onlineIPs, l2tpBatch.GetOnlineIps()...)
	onlineIPs = append(onlineIPs, pptpBatch.GetOnlineIps()...)
	onlineIPs = append(
		onlineIPs,
		ikev2Batch.GetOnlineIps()...,
	)
	onlineIPs = append(onlineIPs, anyConnectBatch.GetOnlineIps()...)

	speeds := make([]*nodev1.UserTrafficSpeed, 0,
		len(coreBatch.GetSpeeds())+
			len(wireGuardBatch.GetSpeeds())+
			len(l2tpBatch.GetSpeeds())+
			len(pptpBatch.GetSpeeds()))
	speeds = append(speeds, awgBatch.GetSpeeds()...)
	speeds = append(speeds, coreBatch.GetSpeeds()...)
	speeds = append(speeds, wireGuardBatch.GetSpeeds()...)
	speeds = append(speeds, l2tpBatch.GetSpeeds()...)
	speeds = append(speeds, pptpBatch.GetSpeeds()...)
	speeds = append(speeds, ikev2Batch.GetSpeeds()...)
	speeds = append(speeds, anyConnectBatch.GetSpeeds()...)

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

	coreAck := true
	if strings.TrimSpace(pending.CoreBatchID) != "" {
		var err error
		coreAck, err = s.ackUsageChildBatch(ctx, pending.CoreBatchID)
		if err != nil {
			return nil, fmt.Errorf("combined core ACK failed: %w", err)
		}
	}

	wgAck := true
	if strings.TrimSpace(pending.WireGuardBatchID) != "" {
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
		wgAck = wgResp.GetAcknowledged()
	}

	l2tpAck := true
	if strings.TrimSpace(pending.L2TPBatchID) != "" {
		resp, err := s.ackL2TPUserUsage(
			ctx,
			&nodev1.AckUsageRequest{
				BatchId: pending.L2TPBatchID,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("combined l2tp ACK failed: %w", err)
		}
		l2tpAck = resp.GetAcknowledged()
	}

	pptpAck := true
	if strings.TrimSpace(pending.PPTPBatchID) != "" {
		resp, err := s.ackPPTPUserUsage(
			ctx,
			&nodev1.AckUsageRequest{
				BatchId: pending.PPTPBatchID,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("combined pptp ACK failed: %w", err)
		}
		pptpAck = resp.GetAcknowledged()
	}
	ikev2Ack := true
	if strings.TrimSpace(pending.IKEv2BatchID) != "" {
		resp, err := s.ackIKEv2UserUsage(
			ctx,
			&nodev1.AckUsageRequest{
				BatchId: pending.IKEv2BatchID,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"combined IKEv2 ACK failed: %w",
				err,
			)
		}
		ikev2Ack = resp.GetAcknowledged()
	}

	awgAck := true
	if strings.TrimSpace(pending.AmneziaWGBatchID) != "" {
		resp, err := s.ackAmneziaWGUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: pending.AmneziaWGBatchID})
		if err != nil {
			return nil, fmt.Errorf("combined AmneziaWG ACK failed: %w", err)
		}
		awgAck = resp.GetAcknowledged()
	}
	anyConnectAck := true
	if strings.TrimSpace(pending.AnyConnectBatchID) != "" {
		resp, err := s.ackAnyConnectUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: pending.AnyConnectBatchID})
		if err != nil {
			return nil, fmt.Errorf("combined AnyConnect ACK failed: %w", err)
		}
		anyConnectAck = resp.GetAcknowledged()
	}

	if !coreAck ||
		!wgAck ||
		!l2tpAck ||
		!pptpAck ||
		!awgAck ||
		!ikev2Ack ||
		!anyConnectAck {
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
	case strings.HasPrefix(batchID, "l2tp-"):
		resp, err := s.ackL2TPUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "pptp-"):
		resp, err := s.ackPPTPUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "ikev2-"):
		resp, err := s.ackIKEv2UserUsage(ctx, req)
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
	case strings.HasPrefix(batchID, "amneziawg-"):
		resp, err := s.ackAmneziaWGUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	case strings.HasPrefix(batchID, "anyconnect-"):
		resp, err := s.ackAnyConnectUserUsage(ctx, req)
		return resp.GetAcknowledged(), err
	default:
		return false, nil
	}
}
