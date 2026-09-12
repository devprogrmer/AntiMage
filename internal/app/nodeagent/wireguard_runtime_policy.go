package nodeagent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func wireGuardRuntimePeerPolicy(
	peer wireGuardRuntimePeer,
) nativeSessionUserPolicy {
	var dataLimit int64
	if peer.DataLimit != nil {
		dataLimit = *peer.DataLimit
	}

	var expire int64
	if peer.Expire != nil {
		expire = *peer.Expire
	}

	return nativeSessionUserPolicy{
		Status:                strings.ToLower(strings.TrimSpace(peer.Status)),
		UsedTraffic:           peer.UsedTraffic,
		ReflectedUsageBatchID: strings.TrimSpace(peer.ReflectedUsageBatchID),
		DataLimit:             dataLimit,
		Expire:                expire,
	}
}

// filterWireGuardRuntimeInboundByStaticPolicy removes peers that are already
// denied by authoritative runtime state. Dynamic policy such as live local
// quota and device admission is intentionally handled separately.
func filterWireGuardRuntimeInboundByStaticPolicy(
	inbound wireGuardRuntimeInbound,
) (wireGuardRuntimeInbound, []wireGuardRuntimePeer) {
	filtered := inbound
	filtered.Peers = make(
		[]wireGuardRuntimePeer,
		0,
		len(inbound.Peers),
	)

	suppressed := make(
		[]wireGuardRuntimePeer,
		0,
		len(inbound.Peers),
	)

	now := time.Now().UTC()

	for _, peer := range inbound.Peers {
		allowed, _ := nativeSessionUserPolicyAllowed(
			wireGuardRuntimePeerPolicy(peer),
			now,
		)

		if allowed {
			filtered.Peers = append(filtered.Peers, peer)
			continue
		}

		suppressed = append(suppressed, peer)
	}

	return filtered, suppressed
}

func wireGuardDynamicSuppressionKey(inboundTag, publicKey string) string {
	return strings.TrimSpace(inboundTag) + "\x00" + strings.TrimSpace(publicKey)
}

func (s *Server) suppressWireGuardPeerUntilDesiredRemoval(
	inboundTag,
	publicKey string,
) {
	key := wireGuardDynamicSuppressionKey(inboundTag, publicKey)
	if key == "\x00" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wireGuardDynamicSuppressedPeers == nil {
		s.wireGuardDynamicSuppressedPeers = make(map[string]struct{})
	}
	s.wireGuardDynamicSuppressedPeers[key] = struct{}{}
}

func (s *Server) filterWireGuardRuntimeInboundByDynamicSuppression(
	inbound wireGuardRuntimeInbound,
) wireGuardRuntimeInbound {
	if len(inbound.Peers) == 0 {
		return inbound
	}

	tag := strings.TrimSpace(inbound.Tag)
	desired := make(map[string]struct{}, len(inbound.Peers))
	filtered := inbound
	filtered.Peers = make([]wireGuardRuntimePeer, 0, len(inbound.Peers))

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, peer := range inbound.Peers {
		key := wireGuardDynamicSuppressionKey(tag, peer.PublicKey)
		desired[key] = struct{}{}
		if _, suppressed := s.wireGuardDynamicSuppressedPeers[key]; suppressed {
			continue
		}
		filtered.Peers = append(filtered.Peers, peer)
	}

	for key := range s.wireGuardDynamicSuppressedPeers {
		if suppressedTag, _, ok := strings.Cut(key, "\x00"); ok &&
			suppressedTag == tag {
			if _, stillDesired := desired[key]; !stillDesired {
				delete(s.wireGuardDynamicSuppressedPeers, key)
			}
		}
	}

	return filtered
}

// snapshotWireGuardSuppressedPeerUsage is called immediately before syncconf.
// If accounting is enabled, any denied peer still present in the kernel gets
// one final persisted checkpoint before syncconf is allowed to remove it.
func (s *Server) snapshotWireGuardSuppressedPeerUsage(
	ctx context.Context,
	prepared preparedWireGuardRuntime,
) error {
	if len(prepared.SuppressedPeers) == 0 {
		return nil
	}

	if !wireGuardBoolSetting(
		prepared.Inbound.Settings,
		"accounting_enabled",
		true,
	) {
		return nil
	}

	raw, err := wireGuardDumpInterface(
		ctx,
		prepared.InterfaceName,
	)
	if err != nil {
		return fmt.Errorf(
			"query interface counters: %w",
			err,
		)
	}

	observedPeers, err := parseWireGuardDump(string(raw))
	if err != nil {
		return fmt.Errorf(
			"parse interface counters: %w",
			err,
		)
	}

	observedByKey := make(
		map[string]wireGuardPeerCounters,
		len(observedPeers),
	)

	for _, peer := range observedPeers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		if publicKey == "" {
			continue
		}
		observedByKey[publicKey] = peer
	}

	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()

	if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		return err
	}

	for _, suppressed := range prepared.SuppressedPeers {
		publicKey := strings.TrimSpace(suppressed.PublicKey)
		if publicKey == "" || suppressed.UserID <= 0 {
			continue
		}

		observed, ok := observedByKey[publicKey]
		if !ok {
			continue
		}

		total, err := wireGuardPeerTotalBytes(observed)
		if err != nil {
			return fmt.Errorf(
				"snapshot peer %q counters: %w",
				publicKey,
				err,
			)
		}

		if err := s.recordWireGuardUsageCarryLocked(
			prepared.Tag,
			prepared.InterfaceName,
			publicKey,
			suppressed.UserID,
			total,
		); err != nil {
			return fmt.Errorf(
				"persist peer %q usage: %w",
				publicKey,
				err,
			)
		}
	}

	return nil
}
