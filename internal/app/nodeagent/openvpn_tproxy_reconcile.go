package nodeagent

import (
	"context"
	"fmt"
	"time"
)

func (s *Server) reconcileOpenVPNTProxyStartup(
	prepared []preparedOpenVPNRuntime,
) error {
	s.mu.Lock()
	if s.openVPNTProxyStartupReconciled {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	hasEnabled := false

	for _, runtime := range prepared {
		if runtime.TProxy.Enabled {
			hasEnabled = true
			break
		}
	}

	// No desired TPROXY state: remove any AntiMage-owned
	// chain/policy state left behind by a previous process.
	if !hasEnabled {
		s.cleanupOpenVPNTProxyPolicyIfUnused()

		s.mu.Lock()
		s.openVPNTProxyStartupReconciled = true
		s.mu.Unlock()

		return nil
	}

	if openVPNNetworkGOOS != "linux" {
		// ensureOpenVPNTProxy will return the platform error.
		return nil
	}

	iptablesPath, err :=
		openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf(
			"openvpn tproxy startup reconcile: iptables command not installed",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	// If our dedicated chain survived a nodeagent crash,
	// flush only that chain. The PREROUTING jump may remain;
	// ensureOpenVPNTProxyChain will verify/recreate it.
	checkArgs := []string{
		"-w", "5",
		"-t", "mangle",
		"-S", openVPNTProxyChain,
	}

	if _, err := openVPNNetworkRun(
		ctx,
		iptablesPath,
		checkArgs...,
	); err == nil {
		if err := runOpenVPNNetworkRequired(
			ctx,
			iptablesPath,
			"-w", "5",
			"-t", "mangle",
			"-F", openVPNTProxyChain,
		); err != nil {
			return fmt.Errorf(
				"openvpn tproxy startup reconcile: %w",
				err,
			)
		}
	}

	s.mu.Lock()
	s.openVPNTProxyStartupReconciled = true
	s.mu.Unlock()

	return nil
}
