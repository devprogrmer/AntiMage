package nodeagent

import (
	"context"
	"errors"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ManageNetworkOptimization is deliberately fail-closed. A mutation requires
// both independently disabled-by-default feature flags and an authoritative
// active peer binding on this node.
func (s *Server) ManageNetworkOptimization(ctx context.Context, req *nodev1.NetworkOptimizationRequest) (*nodev1.NetworkOptimizationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "optimization request is required")
	}
	if req.GetNodeId() <= 0 || req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "valid node and user identities are required")
	}
	var bindings []ni.Binding
	if req.GetAction() == nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS {
		if !s.cfg.NetworkIntelligenceEnabled || !s.cfg.NetworkOptimizationEnabled {
			return nil, status.Error(codes.FailedPrecondition, "network optimization is disabled")
		}
		if s.networkIntelligenceTracker == nil {
			return nil, status.Error(codes.FailedPrecondition, "authoritative WireGuard ownership is unavailable")
		}
		bindings = s.networkIntelligenceTracker.BindingsForUser(req.GetNodeId(), req.GetUserId())
		if len(bindings) == 0 {
			return nil, status.Error(codes.FailedPrecondition, "user has no authenticated active WireGuard peer on this node")
		}
	} else if req.GetAction() != nodev1.NetworkOptimizationRequest_ROLLBACK && req.GetAction() != nodev1.NetworkOptimizationRequest_LIST_USER {
		return nil, status.Error(codes.InvalidArgument, "unsupported network optimization action")
	}
	s.networkOptimizationMu.Lock()
	defer s.networkOptimizationMu.Unlock()
	response, err := s.manageNetworkOptimizationPlatform(ctx, req, bindings)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return response, nil
}

var errNetworkOptimizationUnsupported = errors.New("network optimization is unsupported on this platform")
