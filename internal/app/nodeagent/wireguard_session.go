package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const wireGuardSessionCallbackTimeout = 6 * time.Second

func wireGuardSessionID(inboundTag, publicKey string) string {
	sum := sha256.Sum256([]byte(
		strings.TrimSpace(inboundTag) + "\x00" +
			strings.TrimSpace(publicKey),
	))
	return "wg-" + hex.EncodeToString(sum[:16])
}

func wireGuardEndpointHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "(none)" {
		return ""
	}

	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		return strings.TrimSpace(host)
	}

	// wg normally formats IPv6 endpoints as [addr]:port. If parsing fails,
	// never expose the complete endpoint including port as a device key.
	return ""
}

func (s *Server) reconcileWireGuardSessions(
	ctx context.Context,
	cfg wireGuardUsageRuntimeConfig,
	interfaceName string,
	peers []wireGuardPeerCounters,
	now time.Time,
) error {
	callbackEnabled := strings.TrimSpace(cfg.Callback.URL) != ""
	if !callbackEnabled && len(cfg.Policies) == 0 {
		return nil
	}

	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return fmt.Errorf("wireguard session interface is empty")
	}

	sessionPeers := s.enforceWireGuardPoliciesLocked(

		cfg,

		interfaceName,

		peers,

		now,
	)

	if !callbackEnabled {

		return nil

	}

	// Stop stale sessions first. This matters because the controller's
	// admission check counts non-ended sessions when enforcing device limits.
	for _, peer := range sessionPeers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		userID := cfg.Peers[publicKey]
		if userID <= 0 || wireGuardHandshakeActive(peer.LatestHandshake, now) {
			continue
		}

		event := wireGuardSessionEvent(
			cfg,
			peer,
			userID,
			"stop",
		)

		callbackCtx, cancel := context.WithTimeout(
			ctx,
			wireGuardSessionCallbackTimeout,
		)
		err := s.sendNativeSessionEvent(
			callbackCtx,
			cfg.Callback,
			event,
		)
		cancel()

		if err != nil {
			s.appendLog(
				"wireguard stop callback failed for " +
					cfg.InboundTag + " user " +
					fmt.Sprint(userID) + ": " + err.Error(),
			)
		}
	}

	// Refresh active peers after stale sessions are closed.
	for _, peer := range sessionPeers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		userID := cfg.Peers[publicKey]
		if userID <= 0 || !wireGuardHandshakeActive(peer.LatestHandshake, now) {
			continue
		}

		event := wireGuardSessionEvent(
			cfg,
			peer,
			userID,
			"seen",
		)

		callbackCtx, cancel := context.WithTimeout(
			ctx,
			wireGuardSessionCallbackTimeout,
		)
		err := s.sendNativeSessionEvent(
			callbackCtx,
			cfg.Callback,
			event,
		)
		cancel()

		if err == nil {
			continue
		}

		if !errors.Is(err, errNativeSessionDeviceLimit) {
			s.appendLog(
				"wireguard seen callback failed for " +
					cfg.InboundTag + " user " +
					fmt.Sprint(userID) + ": " + err.Error(),
			)
			continue
		}
		if err := s.disconnectWireGuardPeerAccountingSafeLocked(
			cfg,
			interfaceName,
			peer,
			userID,
		); err != nil {
			s.appendLog(
				"wireguard peer disconnect blocked for " +
					cfg.InboundTag + " user " +
					fmt.Sprint(userID) + ": " + err.Error(),
			)
			continue
		}

		s.appendLog(
			"wireguard peer disconnected by device limit: " +
				cfg.InboundTag + " user " +
				fmt.Sprint(userID),
		)
	}

	return nil
}

func (s *Server) disconnectWireGuardPeerAccountingSafeLocked(
	cfg wireGuardUsageRuntimeConfig,
	interfaceName string,
	peer wireGuardPeerCounters,
	userID int64,
) error {
	publicKey := strings.TrimSpace(peer.PublicKey)

	if wireGuardUsageAccountingEnabled(cfg) {
		total, err := wireGuardPeerTotalBytes(peer)
		if err != nil {
			return fmt.Errorf(
				"snapshot wireguard peer usage before disconnect: %w",
				err,
			)
		}

		if err := s.recordWireGuardUsageCarryLocked(
			cfg.InboundTag,
			interfaceName,
			publicKey,
			userID,
			total,
		); err != nil {
			return fmt.Errorf(
				"persist wireguard peer usage before disconnect: %w",
				err,
			)
		}
	}

	if err := removeWireGuardPeer(interfaceName, publicKey); err != nil {
		return err
	}

	s.suppressWireGuardPeerUntilDesiredRemoval(cfg.InboundTag, publicKey)

	return nil
}

func wireGuardSessionEvent(
	cfg wireGuardUsageRuntimeConfig,
	peer wireGuardPeerCounters,
	userID int64,
	event string,
) nativeSessionEvent {
	publicKey := strings.TrimSpace(peer.PublicKey)

	return nativeSessionEvent{
		UserID:     userID,
		Protocol:   "wg",
		InboundTag: strings.TrimSpace(cfg.InboundTag),
		SessionID:  wireGuardSessionID(cfg.InboundTag, publicKey),
		AssignedIP: strings.TrimSpace(cfg.PeerAddresses[publicKey]),
		ClientIP:   wireGuardEndpointHost(peer.Endpoint),
		Event:      event,
	}
}

func removeWireGuardPeer(
	interfaceName,
	publicKey string,
) error {
	interfaceName = strings.TrimSpace(interfaceName)
	publicKey = strings.TrimSpace(publicKey)

	if interfaceName == "" {
		return fmt.Errorf("wireguard interface is empty")
	}
	if publicKey == "" {
		return fmt.Errorf("wireguard public key is empty")
	}

	wgPath, err := wireGuardRuntimeLookPath("wg")
	if err != nil {
		return fmt.Errorf("wireguard runtime: wg command not installed")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := runWireGuardRuntimeRequired(
		ctx,
		wgPath,
		"set",
		interfaceName,
		"peer",
		publicKey,
		"remove",
	); err != nil {
		return fmt.Errorf(
			"remove wireguard peer %q from %q: %w",
			publicKey,
			interfaceName,
			err,
		)
	}

	return nil
}
