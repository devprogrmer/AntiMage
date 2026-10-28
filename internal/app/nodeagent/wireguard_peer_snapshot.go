package nodeagent

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type wireGuardKernelPeer struct {
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	AllowedIPs          []string
	ReceivedBytes       uint64
	SentBytes           uint64
	PersistentKeepalive uint16
}

// parseWireGuardPeerSnapshot requires a complete single-interface wg dump.
// It retains configurable peer state; handshake and transfer counters are
// parsed for integrity but are intentionally not rollback state.
func parseWireGuardPeerSnapshot(raw string) (map[string]wireGuardKernelPeer, error) {
	peers := make(map[string]wireGuardKernelPeer)
	interfaceRows := 0
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 4 {
			interfaceRows++
			continue
		}
		if len(parts) != 8 {
			return nil, fmt.Errorf("wireguard dump row has %d fields, want 8", len(parts))
		}
		key := strings.TrimSpace(parts[0])
		if err := validateWireGuardKey(key, "snapshot peer public key", false); err != nil {
			return nil, err
		}
		if _, exists := peers[key]; exists {
			return nil, fmt.Errorf("wireguard dump contains duplicate peer public key")
		}
		psk := normalizeWireGuardDumpValue(parts[1])
		if psk != "" {
			if err := validateWireGuardKey(psk, "snapshot peer preshared key", false); err != nil {
				return nil, err
			}
		}
		allowedIPs := []string{}
		if rawAllowedIPs := strings.TrimSpace(parts[3]); rawAllowedIPs != "" {
			for _, rawPrefix := range strings.Split(rawAllowedIPs, ",") {
				prefix, err := netip.ParsePrefix(strings.TrimSpace(rawPrefix))
				if err != nil {
					return nil, fmt.Errorf("wireguard peer %q has invalid allowed IP %q", key, rawPrefix)
				}
				allowedIPs = append(allowedIPs, prefix.String())
			}
		}
		if _, err := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64); err != nil {
			return nil, fmt.Errorf("wireguard peer %q has invalid handshake timestamp", key)
		}
		received, err := strconv.ParseUint(strings.TrimSpace(parts[5]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wireguard peer %q has invalid received-byte counter", key)
		}
		sent, err := strconv.ParseUint(strings.TrimSpace(parts[6]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wireguard peer %q has invalid sent-byte counter", key)
		}
		keepaliveRaw := strings.TrimSpace(parts[7])
		var keepalive uint64
		if !strings.EqualFold(keepaliveRaw, "off") {
			keepalive, err = strconv.ParseUint(keepaliveRaw, 10, 16)
			if err != nil {
				return nil, fmt.Errorf("wireguard peer %q has invalid keepalive", key)
			}
		}
		peers[key] = wireGuardKernelPeer{
			PublicKey:           key,
			PresharedKey:        psk,
			Endpoint:            normalizeWireGuardDumpValue(parts[2]),
			AllowedIPs:          allowedIPs,
			ReceivedBytes:       received,
			SentBytes:           sent,
			PersistentKeepalive: uint16(keepalive),
		}
	}
	if interfaceRows != 1 {
		return nil, fmt.Errorf("wireguard dump contains %d interface rows, want exactly 1", interfaceRows)
	}
	return peers, nil
}

func normalizeWireGuardDumpValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "(none)" {
		return ""
	}
	return value
}

func parseWireGuardPeerList(raw string) (map[string]struct{}, error) {
	peers := make(map[string]struct{})
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		key := strings.TrimSpace(line)
		if key == "" {
			continue
		}
		if err := validateWireGuardKey(key, "peer inventory public key", false); err != nil {
			return nil, err
		}
		if _, exists := peers[key]; exists {
			return nil, fmt.Errorf("wireguard peer inventory contains duplicate key")
		}
		peers[key] = struct{}{}
	}
	return peers, nil
}

func wireGuardPeerInventoriesMatch(snapshot map[string]wireGuardKernelPeer, listed map[string]struct{}) bool {
	if len(snapshot) != len(listed) {
		return false
	}
	for key := range snapshot {
		if _, exists := listed[key]; !exists {
			return false
		}
	}
	return true
}

// wireGuardPeerSnapshotMatchesPolicy refuses to mutate an interface whose
// peer configuration has drifted from the last committed policy. Handshake,
// endpoint, counters and keepalive are runtime state and are intentionally
// excluded from this policy comparison.
func wireGuardPeerSnapshotMatchesPolicy(snapshot map[string]wireGuardKernelPeer, peers []wireGuardRuntimePeer, globalPSK string) error {
	if len(snapshot) != len(peers) {
		return fmt.Errorf("WireGuard kernel peer count differs from committed policy")
	}
	for _, policyPeer := range peers {
		key := strings.TrimSpace(policyPeer.PublicKey)
		kernelPeer, ok := snapshot[key]
		if !ok {
			return fmt.Errorf("WireGuard kernel peer %q is missing from committed policy snapshot", key)
		}
		wantPSK := strings.TrimSpace(policyPeer.PresharedKey)
		if wantPSK == "" {
			wantPSK = strings.TrimSpace(globalPSK)
		}
		address, err := netip.ParseAddr(strings.TrimSpace(policyPeer.Address))
		if err != nil || !address.Is4() {
			return fmt.Errorf("committed WireGuard peer %q has invalid IPv4 address", key)
		}
		if kernelPeer.PresharedKey != wantPSK || len(kernelPeer.AllowedIPs) != 1 || kernelPeer.AllowedIPs[0] != address.String()+"/32" {
			return fmt.Errorf("WireGuard kernel peer %q configuration differs from committed policy", key)
		}
	}
	return nil
}
