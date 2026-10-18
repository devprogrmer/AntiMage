package nodeagent

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/platform/usagereflection"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RetireAccountingAnchors applies only proofs whose durable owner totals match
// this node's accounting ledger. The ordinary transport ACK path never calls it.
func (s *Server) RetireAccountingAnchors(_ context.Context, req *nodev1.RetireAccountingAnchorsRequest) (*nodev1.RetireAccountingAnchorsResponse, error) {
	if req == nil || len(req.GetProofs()) == 0 {
		return &nodev1.RetireAccountingAnchorsResponse{Acknowledged: true}, nil
	}
	nodeID, err := s.currentAccountingNodeID()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "validate local node identity: %v", err)
	}
	s.localUsageMu.Lock()
	defer s.localUsageMu.Unlock()
	if err := s.ensureLocalUsageDeliveriesLocked(); err != nil {
		return nil, status.Errorf(codes.Internal, "load local accounting state: %v", err)
	}
	ledger, err := s.migratedLocalUsageV2Locked()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "migrate local accounting state: %v", err)
	}
	oldLedger, oldDeliveries, oldHash := s.localUsageV2, s.localUsageDeliveries, s.localUsageLegacyHash
	idx := &localUsageV2Index{Owners: make(map[localUsageOwnerKey]int, len(ledger.Owners)), Users: make(map[int64][]int)}
	for i, owner := range ledger.Owners {
		key := localUsageOwnerKey{owner.Protocol, owner.UserID, owner.InboundTag}
		idx.Owners[key] = i
		idx.Users[owner.UserID] = append(idx.Users[owner.UserID], i)
	}
	seenIDs := make(map[string]*nodev1.AccountingRetirementProof, len(req.GetProofs()))
	seenOwners := make(map[localUsageOwnerKey]*nodev1.AccountingRetirementProof, len(req.GetProofs()))
	retire := make(map[localUsageOwnerKey]bool, len(req.GetProofs()))
	accepted := make([]string, 0, len(req.GetProofs()))
	for _, proof := range req.GetProofs() {
		if proof == nil || proof.GetUserId() <= 0 || proof.GetEpoch() != ledger.Epoch || proof.GetOwnerKey() == "" || proof.GetTotalRaw() == 0 || proof.GetLegacyRaw() == 0 || proof.GetLegacyRaw() > proof.GetTotalRaw() {
			return nil, status.Error(codes.InvalidArgument, "invalid accounting retirement proof")
		}
		id, err := hex.DecodeString(proof.GetProofId())
		if err != nil || len(id) != 32 || proof.GetNodeId() != nodeID || proof.GetProofId() != usagereflection.RetirementProofID(nodeID, proof.GetUserId(), proof.GetEpoch(), proof.GetOwnerKey(), proof.GetTotalRaw(), proof.GetLegacyRaw()) {
			return nil, status.Error(codes.InvalidArgument, "accounting retirement proof identity mismatch")
		}
		protocol, tag, err := usagereflection.ParseKey(proof.GetOwnerKey())
		if err != nil || protocol == "" {
			return nil, status.Error(codes.InvalidArgument, "invalid accounting retirement owner")
		}
		key := localUsageOwnerKey{Protocol: protocol, UserID: proof.GetUserId(), InboundTag: tag}
		if previous := seenIDs[proof.GetProofId()]; previous != nil {
			if previous.GetUserId() != proof.GetUserId() || previous.GetEpoch() != proof.GetEpoch() || previous.GetOwnerKey() != proof.GetOwnerKey() || previous.GetTotalRaw() != proof.GetTotalRaw() || previous.GetLegacyRaw() != proof.GetLegacyRaw() {
				return nil, status.Error(codes.InvalidArgument, "conflicting duplicate accounting retirement proof")
			}
			continue
		}
		if seenOwners[key] != nil {
			return nil, status.Error(codes.InvalidArgument, "multiple retirement proofs for one owner in a request")
		}
		seenIDs[proof.GetProofId()] = proof
		seenOwners[key] = proof
		pos, ok := idx.Owners[key]
		if !ok {
			return nil, status.Error(codes.FailedPrecondition, "retirement proof owner has no local durable ledger")
		}
		owner := ledger.Owners[pos]
		// Requiring exact equality prevents a proof from retiring an anchor while
		// newer raw bytes remain only in the node's local cumulative state.
		if proof.GetTotalRaw() != owner.Total {
			return nil, status.Error(codes.FailedPrecondition, "retirement proof does not cover the current local accounting total")
		}
		if owner.RetiredLegacyRaw == 0 {
			var anchorRaw uint64
			for _, delivery := range s.localUsageDeliveries {
				for _, row := range delivery.Rows {
					if row.Protocol == protocol && row.UserID == proof.GetUserId() && row.InboundTag == tag {
						if ^uint64(0)-anchorRaw < row.Raw {
							return nil, status.Error(codes.FailedPrecondition, "local legacy anchor total overflow")
						}
						anchorRaw += row.Raw
					}
				}
			}
			// A prior committed cumulative reflection may already have compacted
			// this exact legacy anchor. In that case there are no bytes to remove;
			// persist the proof identity as an idempotent completion marker.
			if owner.LegacyTotal == 0 && anchorRaw == 0 {
				// Safe no-op: there is no legacy delivery row left to delete.
			} else if owner.LegacyTotal != proof.GetLegacyRaw() || anchorRaw != proof.GetLegacyRaw() {
				return nil, status.Error(codes.FailedPrecondition, "local legacy anchor bytes do not match retirement proof")
			}
		} else if owner.RetiredLegacyRaw != proof.GetLegacyRaw() {
			return nil, status.Error(codes.FailedPrecondition, "retirement proof legacy total conflicts with prior retirement")
		}
		if proof.GetTotalRaw() < owner.RetiredThrough || (proof.GetTotalRaw() == owner.RetiredThrough && owner.RetirementProofID != "" && owner.RetirementProofID != proof.GetProofId()) {
			return nil, status.Error(codes.FailedPrecondition, "stale or conflicting accounting retirement proof")
		}
		retire[key] = true
		accepted = append(accepted, proof.GetProofId())
	}

	// Keep the cumulative owner total unchanged. Remove only the validated fixed
	// legacy rows, whose aggregate was checked byte-for-byte above.
	nextLedger := *ledger
	nextLedger.Owners = append([]localUsageOwnerV2(nil), ledger.Owners...)
	for key := range retire {
		pos := idx.Owners[key]
		owner := &nextLedger.Owners[pos]
		proof := seenOwners[key]
		if owner.RetiredLegacyRaw == 0 {
			owner.RetiredLegacyRaw = proof.GetLegacyRaw()
		}
		owner.LegacyTotal = 0
		owner.RetiredThrough = proof.GetTotalRaw()
		owner.RetirementProofID = proof.GetProofId()
	}
	nextDeliveries := make([]localUsageDelivery, 0, len(oldDeliveries))
	for _, delivery := range oldDeliveries {
		rows := make([]localUsageDeliveryRow, 0, len(delivery.Rows))
		for _, row := range delivery.Rows {
			if !retire[localUsageOwnerKey{Protocol: row.Protocol, UserID: row.UserID, InboundTag: row.InboundTag}] {
				rows = append(rows, row)
			}
		}
		if len(rows) > 0 {
			delivery.Rows = rows
			nextDeliveries = append(nextDeliveries, delivery)
		}
	}
	s.localUsageV2 = &nextLedger
	if err := validateLocalUsageV2(s.localUsageV2); err != nil {
		s.localUsageV2 = oldLedger
		return nil, status.Errorf(codes.Internal, "validate retirement state: %v", err)
	}
	if err := s.persistLocalUsageDeliveriesLocked(nextDeliveries); err != nil {
		s.localUsageV2, s.localUsageDeliveries, s.localUsageLegacyHash = oldLedger, oldDeliveries, oldHash
		s.localUsageV2Index = nil
		s.localUsageDeliveryIndex = nil
		return nil, status.Errorf(codes.Internal, "persist accounting retirement state: %v", err)
	}
	s.localUsageDeliveries = nextDeliveries
	s.localUsageV2Index = nil
	s.localUsageDeliveryIndex = nil
	return &nodev1.RetireAccountingAnchorsResponse{Acknowledged: true, AcceptedProofIds: accepted}, nil
}

func (s *Server) currentAccountingNodeID() (int64, error) {
	policy, err := s.currentRuntimePolicy()
	if err != nil {
		return 0, err
	}
	if policy == nil {
		return 0, status.Error(codes.FailedPrecondition, "no durable runtime policy is installed")
	}
	payload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil {
		return 0, err
	}
	nodeID := payload.SessionCallback.NodeID
	if nodeID <= 0 {
		return 0, status.Error(codes.FailedPrecondition, "runtime policy has no trusted node identity")
	}
	if strings.HasPrefix(payload.Target, "node:") {
		targetID, err := strconv.ParseInt(strings.TrimPrefix(payload.Target, "node:"), 10, 64)
		if err != nil || targetID != nodeID {
			return 0, status.Error(codes.FailedPrecondition, "runtime target and session callback node identities differ")
		}
	}
	return nodeID, nil
}
