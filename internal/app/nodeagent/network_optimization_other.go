//go:build !linux

package nodeagent

import (
	"context"
	"errors"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func (s *Server) manageNetworkOptimizationPlatform(context.Context, *nodev1.NetworkOptimizationRequest, []ni.Binding) (*nodev1.NetworkOptimizationResponse, error) {
	return nil, errNetworkOptimizationUnsupported
}

func resetNetworkOptimizationsForInterface(string, string) error {
	return errors.New("network optimization is unsupported on this platform")
}
