package nodeagent

import (
	"context"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func (s *Server) collectUserUsageWithWireGuard(
	ctx context.Context,
	req *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	coreBatch, coreErr := s.collectCoreUserUsage(ctx, req)
	wgBatch, wgErr := s.collectWireGuardUserUsage(ctx, req)

	if coreErr != nil && wgErr != nil {
		return nil, coreErr
	}
	if coreErr != nil {
		return wgBatch, nil
	}
	if wgErr != nil {
		return coreBatch, nil
	}
	return s.combineUserUsageBatches(coreBatch, wgBatch)
}

func (s *Server) collectCoreUserUsage(
	ctx context.Context,
	req *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	ovpnBatch, ovpnErr := s.collectOpenVPNUserUsage(ctx, req)
	xrayBatch, xrayErr := s.collectXrayUserUsage(ctx, req)

	if ovpnErr != nil && xrayErr != nil {
		return nil, ovpnErr
	}
	if ovpnErr != nil {
		return xrayBatch, nil
	}
	if xrayErr != nil {
		return ovpnBatch, nil
	}
	return s.mergeUserUsageBatches(ovpnBatch, xrayBatch), nil
}
