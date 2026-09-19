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
	awgBatch, awgErr := s.collectAmneziaWGUserUsage(ctx, req)
	l2tpBatch, l2tpErr := s.collectL2TPUserUsage(ctx, req)
	pptpBatch, pptpErr := s.collectPPTPUserUsage(ctx, req)

	if coreErr != nil && wgErr != nil && awgErr != nil && l2tpErr != nil && pptpErr != nil {
		return nil, coreErr
	}
	if coreErr != nil {
		coreBatch = &nodev1.UserUsageBatch{}
	}
	if wgErr != nil {
		wgBatch = &nodev1.UserUsageBatch{}
	}
	if awgErr != nil {
		awgBatch = &nodev1.UserUsageBatch{}
	}
	if l2tpErr != nil {
		l2tpBatch = &nodev1.UserUsageBatch{}
	}
	if pptpErr != nil {
		pptpBatch = &nodev1.UserUsageBatch{}
	}
	return s.combineUserUsageBatches(
		coreBatch,
		wgBatch,
		l2tpBatch,
		pptpBatch,
		awgBatch,
	)
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
