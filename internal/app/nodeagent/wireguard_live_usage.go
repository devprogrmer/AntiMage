package nodeagent

import (
	"fmt"
	"strings"
)

// wireGuardLiveUnackedUsageLocked returns usage for one user/inbound that
// has been observed locally but is not yet safely represented by the
// controller's persisted UsedTraffic value.
//
// The caller must hold wireGuardUsageMu and must have loaded usage state.
func (s *Server) wireGuardLiveUnackedUsageLocked(
	cfg wireGuardUsageRuntimeConfig,
	interfaceName string,
	peers []wireGuardPeerCounters,
	userID int64,
) (uint64, error) {
	if userID <= 0 {
		return 0, fmt.Errorf(
			"wireguard live usage user id is invalid",
		)
	}

	inboundTag := strings.TrimSpace(cfg.InboundTag)
	if inboundTag == "" {
		return 0, fmt.Errorf(
			"wireguard live usage inbound tag is empty",
		)
	}

	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return 0, fmt.Errorf(
			"wireguard live usage interface is empty",
		)
	}

	var live uint64

	add := func(value uint64, source string) error {
		if ^uint64(0)-live < value {
			return fmt.Errorf(
				"wireguard live usage overflow while adding %s for user %d",
				source,
				userID,
			)
		}

		live += value
		return nil
	}

	pending := s.wireGuardUsagePending

	reflectedBatchID := ""
	for publicKey, mappedUserID := range cfg.Peers {
		if mappedUserID != userID {
			continue
		}

		policy, ok := cfg.Policies[publicKey]
		if !ok {
			continue
		}

		candidate := strings.TrimSpace(policy.ReflectedUsageBatchID)
		if candidate == "" {
			continue
		}

		if reflectedBatchID != "" && reflectedBatchID != candidate {
			return 0, fmt.Errorf(
				"wireguard live usage reflected batch mismatch for user %d: %q != %q",
				userID,
				reflectedBatchID,
				candidate,
			)
		}

		reflectedBatchID = candidate
	}

	pendingReflected := pending != nil &&
		reflectedBatchID != "" &&
		strings.TrimSpace(pending.BatchID) == reflectedBatchID

	// Pending samples have already been collected locally but have not
	// received a controller ACK yet. If this exact batch is already
	// reflected in authoritative UsedTraffic, do not add it again.
	if pending != nil && !pendingReflected {
		for _, sample := range pending.Samples {
			if sample.UserID != userID ||
				strings.TrimSpace(sample.InboundTag) != inboundTag {
				continue
			}

			if err := add(sample.Value, "pending usage"); err != nil {
				return 0, err
			}
		}
	}

	awaitingUsage, err := s.wireGuardAwaitingReflectionUsageLocked(
		inboundTag,
		userID,
		reflectedBatchID,
	)
	if err != nil {
		return 0, err
	}
	if err := add(awaitingUsage, "ACKed unreflected usage"); err != nil {
		return 0, err
	}

	// Carry may contain bytes that were snapshotted during a safe detach.
	// A portion of that carry can already be present in the immutable
	// pending batch. Only add the remainder here.
	for baselineKey, carry := range s.wireGuardUsageCarry {
		if carry.UserID != userID ||
			strings.TrimSpace(carry.InboundTag) != inboundTag {
			continue
		}

		remaining := carry.Value

		if pending != nil {
			consumed := pending.CarryValues[baselineKey]
			if consumed > remaining {
				return 0, fmt.Errorf(
					"wireguard live usage carry underflow for key %q: have %d, pending consumed %d",
					baselineKey,
					remaining,
					consumed,
				)
			}

			remaining -= consumed
		}

		if err := add(remaining, "carry usage"); err != nil {
			return 0, err
		}
	}

	// Finally add bytes observed in the live kernel counters after the
	// newest logical checkpoint for each current peer.
	for _, peer := range peers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		if cfg.Peers[publicKey] != userID {
			continue
		}

		total, err := wireGuardPeerTotalBytes(peer)
		if err != nil {
			return 0, fmt.Errorf(
				"wireguard live usage counters for user %d peer %q: %w",
				userID,
				publicKey,
				err,
			)
		}

		baselineKey := wireGuardUsageBaselineKey(
			inboundTag,
			interfaceName,
			publicKey,
		)

		var (
			reference uint64
			hasRef    bool
		)

		if carry, ok := s.wireGuardUsageCarry[baselineKey]; ok {
			if carry.UserID != userID ||
				strings.TrimSpace(carry.InboundTag) != inboundTag {
				return 0, fmt.Errorf(
					"wireguard live usage carry identity mismatch for key %q",
					baselineKey,
				)
			}

			reference = carry.NextBaseline
			hasRef = true
		} else if pending != nil {
			if value, ok := pending.NextBaseline[baselineKey]; ok {
				reference = value
				hasRef = true
			}
		}

		if !hasRef {
			if value, ok := s.wireGuardUsageBaseline[baselineKey]; ok {
				reference = value
				hasRef = true
			}
		}

		delta := total

		if hasRef && total >= reference {
			delta = total - reference
		}

		// WireGuard counters can restart from zero after peer recreation.
		// In that case the current total belongs to the new counter epoch.
		if hasRef && total < reference {
			delta = total
		}

		if err := add(delta, "kernel usage"); err != nil {
			return 0, err
		}
	}

	return live, nil
}
