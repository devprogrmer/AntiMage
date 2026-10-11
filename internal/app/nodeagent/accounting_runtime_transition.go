package nodeagent

import (
	"context"
	"fmt"
	"time"
)

func (s *Server) checkpointBeforeRuntimeTransition(ctx context.Context) error {
	for _, checkpoint := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"xray", s.checkpointXrayAccounting},
		{"wireguard", func(ctx context.Context) error { return s.wireGuardOfflineTick(ctx, false, true) }},
		{"amneziawg", s.checkpointAmneziaWGOffline},
		{"openvpn", s.checkpointOpenVPNOffline},
		{"ppp", s.checkpointPPPOffline},
		{"ikev2", s.checkpointIKEv2Offline},
		{"anyconnect", s.checkpointAnyConnectOffline},
	} {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := checkpoint.run(bounded)
		cancel()
		if err != nil {
			s.recordLocalAccountingHealth(checkpoint.name, err)
			return fmt.Errorf("%s checkpoint before runtime transition: %w", checkpoint.name, err)
		}
	}
	return nil
}
