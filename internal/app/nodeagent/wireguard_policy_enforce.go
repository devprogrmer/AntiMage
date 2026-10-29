package nodeagent

import (
	"errors"
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
	var enforcementErr error
	defer func() { s.recordLocalAccountingHealth("wireguard-policy", enforcementErr) }()
	if len(cfg.Policies) == 0 {
		return peers
	}

	remaining := make(
		[]wireGuardPeerCounters,
		0,
		len(peers),
	)
	ipsByUser := map[int64]map[string]bool{}
	credentialsByUser := map[int64]map[string]bool{}

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
				enforcementErr = errors.Join(enforcementErr, fmt.Errorf("wireguard %s user %d live quota: %w", cfg.InboundTag, userID, err))

				remaining = append(remaining, peer)
				continue
			}

			allowed, reason =
				s.localQuotaAllowed(
					"wireguard", userID, cfg.InboundTag,
					policy,
					liveBytes,
					now,
				)
		}

		if allowed && wireGuardHandshakeActive(peer.LatestHandshake, now) {
			if credentialsByUser[userID] == nil {
				credentialsByUser[userID] = map[string]bool{}
			}
			if policy.DeviceLimit > 0 && !credentialsByUser[userID][publicKey] && int64(len(credentialsByUser[userID])) >= policy.DeviceLimit {
				allowed, reason = false, "device credential limit reached"
			}
			if ipsByUser[userID] == nil {
				ipsByUser[userID] = map[string]bool{}
			}
			if allowed && offlineIPLimitExceeded(ipsByUser[userID], wireGuardEndpointHost(peer.Endpoint), policy.IPLimit) {
				allowed, reason = false, "IP limit reached"
			}
			if allowed {
				credentialsByUser[userID][publicKey] = true
			}
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
			enforcementErr = errors.Join(enforcementErr, fmt.Errorf("wireguard %s user %d disconnect: %w", cfg.InboundTag, userID, err))

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
