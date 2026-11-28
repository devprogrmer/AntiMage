package nodeagent

import (
	"errors"
	"net/netip"
	"strings"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

// verifiedWireGuardBindings joins a managed, applied interface snapshot with
// the exact peer/address set expected by the caller. The caller must hold the
// capture lifecycle barrier and verify identity and ownership before and after
// reading `wg show <interface> allowed-ips`. Desired policy alone is insufficient.
// Interface identity is carried in Session to invalidate interface replacement.
func verifiedWireGuardBindings(nodeID int64, prepared preparedWireGuardRuntime, interfaceIdentity string, generation uint64, since time.Time, allowedIPs string) ([]ni.Binding, error) {
	if nodeID <= 0 || interfaceIdentity == "" || generation == 0 || since.IsZero() || prepared.Tag == "" || !wireGuardInterfaceNamePattern.MatchString(prepared.InterfaceName) {
		return nil, errors.New("invalid verified interface context")
	}
	expected := make(map[string]wireGuardRuntimePeer, len(prepared.Inbound.Peers))
	addresses := make(map[netip.Addr]struct{}, len(prepared.Inbound.Peers))
	for _, peer := range prepared.Inbound.Peers {
		address, err := netip.ParseAddr(peer.Address)
		if err != nil || peer.UserID <= 0 || validateWireGuardKey(peer.PublicKey, "peer public key", false) != nil {
			return nil, errors.New("invalid monitoring peer")
		}
		if _, exists := expected[peer.PublicKey]; exists {
			return nil, errors.New("duplicate monitoring peer")
		}
		if _, exists := addresses[address.Unmap()]; exists {
			return nil, errors.New("ambiguous monitoring address")
		}
		expected[peer.PublicKey] = peer
		addresses[address.Unmap()] = struct{}{}
	}
	seen := make(map[string]struct{}, len(expected))
	result := make([]ni.Binding, 0, len(expected))
	for _, line := range strings.Split(allowedIPs, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, errors.New("invalid kernel allowed IP snapshot")
		}
		peer, exists := expected[fields[0]]
		if !exists {
			return nil, errors.New("unknown kernel peer")
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return nil, errors.New("duplicate kernel peer")
		}
		prefix, err := netip.ParsePrefix(fields[1])
		address := netip.MustParseAddr(peer.Address)
		if err != nil || prefix.Bits() != prefix.Addr().BitLen() || prefix.Addr() != address {
			return nil, errors.New("kernel peer address mismatch")
		}
		seen[fields[0]] = struct{}{}
		result = append(result, ni.Binding{NodeID: nodeID, Interface: prepared.InterfaceName, Address: address.Unmap(), UserID: peer.UserID, Session: interfaceIdentity + "/" + wireGuardSessionID(prepared.Tag, peer.PublicKey), Generation: generation, Since: since})
	}
	if len(seen) != len(expected) {
		return nil, errors.New("missing kernel peer")
	}
	return result, nil
}
