package nodeagent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const defaultWireGuardPoolCIDR = "10.69.0.0/16"

type preparedWireGuardRuntime struct {
	Tag               string
	InterfaceName     string
	ExplicitInterface bool
	ConfigPath        string
	ServerCIDR        string
	SourceCIDR        string
	MTU               int
	Routing           wireGuardRoutingSpec
	Inbound           wireGuardRuntimeInbound
}

type wireGuardRuntimeState struct {
	Tag           string `json:"tag"`
	InterfaceName string `json:"interface_name"`
	ConfigPath    string `json:"config_path"`
	ServerCIDR    string `json:"server_cidr"`
	SourceCIDR    string `json:"source_cidr"`
	MTU           int    `json:"mtu,omitempty"`
}

func wireGuardManagedInterfaceName(
	inbound wireGuardRuntimeInbound,
) (string, error) {
	if explicit, err := wireGuardInterfaceName(inbound); err != nil {
		return "", err
	} else if explicit != "" {
		return explicit, nil
	}

	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return "", fmt.Errorf("wireguard inbound tag is required")
	}
	sum := sha256.Sum256([]byte(tag))
	name := fmt.Sprintf("amwg%x", sum[:4])
	if !wireGuardInterfaceNamePattern.MatchString(name) {
		return "", fmt.Errorf(
			"wireguard %q: generated invalid interface name %q",
			tag,
			name,
		)
	}
	return name, nil
}

func (s *Server) prepareWireGuardInbound(
	inbound wireGuardRuntimeInbound,
) (preparedWireGuardRuntime, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard inbound tag is required",
		)
	}
	if inbound.ListenPort < 1 || inbound.ListenPort > 65535 {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: invalid listen port %d",
			tag,
			inbound.ListenPort,
		)
	}

	interfaceName, err := wireGuardManagedInterfaceName(inbound)
	if err != nil {
		return preparedWireGuardRuntime{}, err
	}

	explicitInterfaceName, err := wireGuardInterfaceName(inbound)
	if err != nil {
		return preparedWireGuardRuntime{}, err
	}

	privateKey := wireGuardStringSetting(inbound.Settings, "private_key")
	if err := validateWireGuardKey(privateKey, "private_key", false); err != nil {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: %w",
			tag,
			err,
		)
	}

	pool, serverCIDR, err := wireGuardRuntimeAddressing(inbound.Settings)
	if err != nil {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: %w",
			tag,
			err,
		)
	}

	globalPSK := firstWireGuardSetting(
		inbound.Settings,
		"pre_shared_key",
		"preshared_key",
		"presharedKey",
	)
	if err := validateWireGuardKey(globalPSK, "preshared_key", true); err != nil {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: %w",
			tag,
			err,
		)
	}

	seenKeys := make(map[string]struct{}, len(inbound.Peers))
	seenAddresses := make(map[string]struct{}, len(inbound.Peers))
	serverPrefix, _ := netip.ParsePrefix(serverCIDR)

	for _, peer := range inbound.Peers {
		publicKey := strings.TrimSpace(peer.PublicKey)
		if err := validateWireGuardKey(
			publicKey,
			"peer public_key",
			false,
		); err != nil {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q user %d: %w",
				tag,
				peer.UserID,
				err,
			)
		}
		if _, exists := seenKeys[publicKey]; exists {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q: duplicate peer public key",
				tag,
			)
		}
		seenKeys[publicKey] = struct{}{}

		addr, err := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if err != nil || !addr.Is4() {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q user %d: invalid IPv4 peer address %q",
				tag,
				peer.UserID,
				peer.Address,
			)
		}
		if !wireGuardUsableAddress(pool, addr) {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q user %d: peer address %s is outside usable pool %s",
				tag,
				peer.UserID,
				addr,
				pool,
			)
		}
		if addr == serverPrefix.Addr() {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q user %d: peer address conflicts with server address",
				tag,
				peer.UserID,
			)
		}
		if _, exists := seenAddresses[addr.String()]; exists {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q: duplicate peer address %s",
				tag,
				addr,
			)
		}
		seenAddresses[addr.String()] = struct{}{}

		psk := strings.TrimSpace(peer.PresharedKey)
		if psk == "" {
			psk = globalPSK
		}
		if err := validateWireGuardKey(
			psk,
			"peer preshared_key",
			true,
		); err != nil {
			return preparedWireGuardRuntime{}, fmt.Errorf(
				"wireguard %q user %d: %w",
				tag,
				peer.UserID,
				err,
			)
		}
	}

	configText, err := renderWireGuardRuntimeConfig(
		inbound,
		privateKey,
		globalPSK,
	)
	if err != nil {
		return preparedWireGuardRuntime{}, err
	}

	dir := filepath.Join(
		s.cfg.DataDir,
		"wireguard",
		"runtime",
		wireGuardRuntimeDirName(tag),
	)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: create runtime directory: %w",
			tag,
			err,
		)
	}

	configPath := filepath.Join(dir, "wg.conf")
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(configText), 0600); err != nil {
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: write runtime config: %w",
			tag,
			err,
		)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return preparedWireGuardRuntime{}, fmt.Errorf(
			"wireguard %q: replace runtime config: %w",
			tag,
			err,
		)
	}

	routing, err := buildWireGuardRoutingSpec(
		inbound,
		interfaceName,
		pool.String(),
	)
	if err != nil {
		return preparedWireGuardRuntime{}, err
	}

	return preparedWireGuardRuntime{
		Tag:               tag,
		InterfaceName:     interfaceName,
		ExplicitInterface: explicitInterfaceName != "",
		ConfigPath:        configPath,
		ServerCIDR:        serverCIDR,
		SourceCIDR:        pool.String(),
		MTU:               wireGuardIntSetting(inbound.Settings, "mtu"),
		Routing:           routing,
		Inbound:           inbound,
	}, nil
}

func renderWireGuardRuntimeConfig(
	inbound wireGuardRuntimeInbound,
	privateKey string,
	globalPSK string,
) (string, error) {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = " + strings.TrimSpace(privateKey) + "\n")
	b.WriteString("ListenPort = " + strconv.Itoa(inbound.ListenPort) + "\n")

	peers := append([]wireGuardRuntimePeer(nil), inbound.Peers...)
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].UserID == peers[j].UserID {
			return peers[i].PublicKey < peers[j].PublicKey
		}
		return peers[i].UserID < peers[j].UserID
	})

	for _, peer := range peers {
		addr, err := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if err != nil || !addr.Is4() {
			return "", fmt.Errorf(
				"wireguard %q user %d: invalid peer address",
				inbound.Tag,
				peer.UserID,
			)
		}
		b.WriteString("\n[Peer]\n")
		b.WriteString("PublicKey = " + strings.TrimSpace(peer.PublicKey) + "\n")
		psk := strings.TrimSpace(peer.PresharedKey)
		if psk == "" {
			psk = globalPSK
		}
		if psk != "" {
			b.WriteString("PresharedKey = " + psk + "\n")
		}
		b.WriteString("AllowedIPs = " + addr.String() + "/32\n")
	}
	return b.String(), nil
}

func wireGuardRuntimeAddressing(
	settings map[string]any,
) (netip.Prefix, string, error) {
	rawPool := firstWireGuardSetting(
		settings,
		"address_pool",
		"ipv4_pool_cidr",
		"ipv4PoolCidr",
	)
	if rawPool == "" {
		rawPool = defaultWireGuardPoolCIDR
	}

	pool, err := netip.ParsePrefix(rawPool)
	if err != nil || !pool.Addr().Is4() || pool.Bits() > 30 {
		return netip.Prefix{}, "", fmt.Errorf(
			"address_pool must be an IPv4 CIDR of /30 or larger",
		)
	}
	pool = pool.Masked()

	serverAddr := pool.Addr().Next()
	rawServer := firstWireGuardSetting(
		settings,
		"server_address",
		"serverAddress",
	)
	if rawServer != "" {
		if prefix, parseErr := netip.ParsePrefix(rawServer); parseErr == nil {
			serverAddr = prefix.Addr()
		} else if addr, parseErr := netip.ParseAddr(rawServer); parseErr == nil {
			serverAddr = addr
		} else {
			return netip.Prefix{}, "", fmt.Errorf(
				"invalid server_address %q",
				rawServer,
			)
		}
	}

	if !serverAddr.Is4() || !wireGuardUsableAddress(pool, serverAddr) {
		return netip.Prefix{}, "", fmt.Errorf(
			"server_address must be a usable address inside address_pool",
		)
	}
	return pool, netip.PrefixFrom(serverAddr, pool.Bits()).String(), nil
}

func wireGuardUsableAddress(pool netip.Prefix, addr netip.Addr) bool {
	if !addr.Is4() || !pool.Contains(addr) {
		return false
	}
	baseBytes := pool.Masked().Addr().As4()
	base := binary.BigEndian.Uint32(baseBytes[:])
	addrBytes := addr.As4()
	value := binary.BigEndian.Uint32(addrBytes[:])
	hostCount := uint64(1) << uint64(32-pool.Bits())
	offset := uint64(value - base)
	return offset > 0 && offset < hostCount-1
}

func validateWireGuardKey(
	value string,
	label string,
	optional bool,
) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s is required", label)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("%s must be a 32-byte base64 key", label)
	}
	return nil
}

func firstWireGuardSetting(
	settings map[string]any,
	keys ...string,
) string {
	for _, key := range keys {
		if value := wireGuardStringSetting(settings, key); value != "" {
			return value
		}
	}
	return ""
}

func wireGuardIntSetting(settings map[string]any, key string) int {
	if settings == nil {
		return 0
	}
	switch value := settings[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func cloneWireGuardRuntimeInboundWithInterface(
	inbound wireGuardRuntimeInbound,
	interfaceName string,
) wireGuardRuntimeInbound {
	out := inbound
	out.Settings = make(map[string]any, len(inbound.Settings)+1)
	for key, value := range inbound.Settings {
		out.Settings[key] = value
	}
	out.Settings["interface_name"] = interfaceName
	return out
}

func (s *Server) wireGuardRuntimeManifestPath(tag string) string {
	return filepath.Join(
		s.cfg.DataDir,
		"wireguard",
		"runtime",
		wireGuardRuntimeDirName(tag),
		"runtime.json",
	)
}

func (s *Server) persistWireGuardRuntimeState(
	state wireGuardRuntimeState,
) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := s.wireGuardRuntimeManifestPath(state.Tag)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) loadWireGuardRuntimeStates() (
	map[string]wireGuardRuntimeState,
	error,
) {
	root := filepath.Join(s.cfg.DataDir, "wireguard", "runtime")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]wireGuardRuntimeState{}, nil
		}
		return nil, err
	}
	result := make(map[string]wireGuardRuntimeState)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(
			root,
			entry.Name(),
			"runtime.json",
		))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var state wireGuardRuntimeState
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
		if strings.TrimSpace(state.Tag) == "" ||
			strings.TrimSpace(state.InterfaceName) == "" {
			continue
		}
		result[state.Tag] = state
	}
	return result, nil
}
