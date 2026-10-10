package nodeagent

import (
	"context"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Native runtime callbacks and installed CLI jobs share the same on-disk locks
// and resource journal. Holding the generation lock makes a destructive boundary
// indivisible with respect to accepting a newer resource owner.
func (s *Server) fencedRuntimeAction(ctx context.Context, id string, fence *nodev1.DestructiveFence, apply func(context.Context) (*nodev1.RuntimeActionResponse, error)) (*nodev1.RuntimeActionResponse, error) {
	if fence == nil {
		return nil, status.Error(codes.FailedPrecondition, "shared destructive fencing is required")
	}
	if id == "" || fence.OperationId != id {
		return nil, status.Error(codes.InvalidArgument, "fencing operation identity mismatch")
	}
	if err := acceptNativeDestructiveFence(ctx, fence); err != nil {
		return nil, err
	}
	response, err := runNativeDestructiveBoundary(ctx, fence, apply)
	if response != nil {
		response.OperationId = id
		response.CommandId = fence.CommandId
		response.ResourceId = fence.ResourceId
		response.ResourceGeneration = fence.ResourceGeneration
		response.LeaseGeneration = fence.LeaseGeneration
	}
	return response, err
}

func (s *Server) UpdateRuntime(ctx context.Context, req *nodev1.RuntimeUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) { return s.updateRuntime(ctx, req) })
}

func (s *Server) UpdateGeo(ctx context.Context, req *nodev1.GeoUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := s.requireBinaryMaintenance(); err != nil {
		return nil, err
	}
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) { return s.updateGeo(ctx, req) })
}

func (s *Server) StartRuntime(ctx context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) {
		return s.applyConfig(ctx, req, "started")
	})
}

func (s *Server) SyncConfig(ctx context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) {
		return s.applyConfig(ctx, req, "config synced")
	})
}

func (s *Server) RestartRuntime(ctx context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) { return s.restartRuntime(ctx, req) })
}

func (s *Server) StopRuntime(ctx context.Context, req *nodev1.StopRuntimeRequest) (*nodev1.RuntimeActionResponse, error) {
	return s.fencedRuntimeAction(ctx, req.GetOperationId(), req.GetFence(), func(ctx context.Context) (*nodev1.RuntimeActionResponse, error) { return s.stopRuntimeAction(ctx, req) })
}
