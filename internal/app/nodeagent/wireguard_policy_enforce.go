package nodeagent

import (
	"fmt"
	"strings"
	"time"
)

// enforceWireGuardPoliciesLocked evaluates runtime policy before session
// callbacks. The caller must hold wireGuardUsageMu and have usage state loaded.
//
// Peers that are successfully disconnected are omitted from the returned slice
// so they cannot immediately emit a "seen" session callback from the same
// stale WireGuard dump.
func (s *Server) enforceWireGuardPoliciesLocked(
	cfg wireGuardUsageRuntimeConfig,
	interfaceName string,
	peers []wireGuardPeerCounters,
	now time.Time,
) []wireGuardPeerCounters {
	if len(cfg.Policies) == 0 {
		return peers
	}

	remaining := make(
		[]wireGuardPeerCounters,
		0,
		len(peers),
	)

	for _, peer := range peers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		userID := cfg.Peers[publicKey]

		if userID <= 0 {
			remaining = append(remaining, peer)
			continue
		}

		policy, ok := cfg.Policies[publicKey]
		if !ok {
			remaining = append(remaining, peer)
			continue
		}

		// First apply policy that does not require live counters:
		// status, expiry and already-persisted quota.
		allowed, reason := nativeSessionUserPolicyAllowed(
			policy,
			now,
		)

		// Only active/otherwise-allowed users need local live usage checked.
		// When accounting is disabled we intentionally preserve static policy
		// enforcement without treating raw kernel counters as unacknowledged
		// billing usage.
		if allowed && wireGuardUsageAccountingEnabled(cfg) {
			liveBytes, err := s.wireGuardLiveUnackedUsageLocked(
				cfg,
				interfaceName,
				peers,
				userID,
			)
			if err != nil {
				s.appendLog(
					"wireguard live policy usage failed for " +
						cfg.InboundTag + " user " +
						fmt.Sprint(userID) + ": " + err.Error(),
				)

				remaining = append(remaining, peer)
				continue
			}

			allowed, reason =
				nativeSessionUserPolicyAllowedWithLiveUsage(
					policy,
					liveBytes,
					now,
				)
		}

		if allowed {
			remaining = append(remaining, peer)
			continue
		}

		if err := s.disconnectWireGuardPeerAccountingSafeLocked(
			cfg,
			interfaceName,
			peer,
			userID,
		); err != nil {
			s.appendLog(
				"wireguard policy disconnect blocked for " +
					cfg.InboundTag + " user " +
					fmt.Sprint(userID) + ": " + err.Error(),
			)

			remaining = append(remaining, peer)
			continue
		}

		s.appendLog(
			"wireguard peer disconnected by policy: " +
				cfg.InboundTag + " user " +
				fmt.Sprint(userID) + ": " + reason,
		)
	}

	return remaining
}
