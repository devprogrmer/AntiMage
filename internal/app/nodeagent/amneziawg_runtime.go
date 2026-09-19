package nodeagent

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const defaultAmneziaWGPoolCIDR = "10.72.0.0/16"

type amneziaWGRuntimeInbound struct {
	Tag        string                 `json:"tag"`
	TunnelTag  string                 `json:"tunnel_tag"`
	ListenPort int                    `json:"listen_port"`
	TunnelPort int                    `json:"tunnel_port"`
	Settings   map[string]any         `json:"settings"`
	Peers      []amneziaWGRuntimePeer `json:"peers"`
}

type amneziaWGRuntimePeer struct {
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	DeviceIndex  int    `json:"device_index"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key,omitempty"`
	Address      string `json:"address"`
	Status       string `json:"status"`
	UsedTraffic  int64  `json:"used_traffic"`
	DataLimit    *int64 `json:"data_limit,omitempty"`
	Expire       *int64 `json:"expire,omitempty"`
	DeviceLimit  int64  `json:"device_limit,omitempty"`
}

type amneziaWGObfuscation struct {
	Jc, Jmin, Jmax, S1, S2 int
	H1, H2, H3, H4         string
}

type preparedAmneziaWGRuntime struct {
	Tag           string
	InterfaceName string
	ConfigPath    string
	ConfigText    string
	ServerCIDR    string
	SourceCIDR    string
	MTU           int
	Obfuscation   amneziaWGObfuscation
	Inbound       amneziaWGRuntimeInbound
}

type amneziaWGRuntimeState struct {
	Tag           string `json:"tag"`
	InterfaceName string `json:"interface_name"`
	ConfigPath    string `json:"config_path"`
	ServerCIDR    string `json:"server_cidr"`
	SourceCIDR    string `json:"source_cidr"`
	MTU           int    `json:"mtu"`
}

func amneziaWGGeneratedInterfaceName(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", fmt.Errorf("amneziawg inbound tag is required")
	}
	sum := sha256.Sum256([]byte(tag))
	name := fmt.Sprintf("awg%x", sum[:4])
	if !wireGuardInterfaceNamePattern.MatchString(name) {
		return "", fmt.Errorf("amneziawg %q: generated invalid interface name %q", tag, name)
	}
	return name, nil
}

func (s *Server) prepareAmneziaWGInbound(inbound amneziaWGRuntimeInbound) (preparedAmneziaWGRuntime, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg inbound tag is required")
	}
	if inbound.ListenPort < 1 || inbound.ListenPort > 65535 {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: invalid listen port %d", tag, inbound.ListenPort)
	}
	privateKey := wireGuardStringSetting(inbound.Settings, "private_key")
	if err := validateWireGuardKey(privateKey, "private_key", false); err != nil {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: %w", tag, err)
	}
	pool, serverCIDR, err := amneziaWGRuntimeAddressing(inbound.Settings)
	if err != nil {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: %w", tag, err)
	}
	mtu := wireGuardIntSetting(inbound.Settings, "mtu")
	if mtu == 0 {
		mtu = 1420
	}
	if mtu < 576 || mtu > 1500 {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: invalid MTU %d", tag, mtu)
	}
	obfs, err := amneziaWGObfuscationSettings(inbound.Settings)
	if err != nil {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: %w", tag, err)
	}
	seenKeys := make(map[string]struct{}, len(inbound.Peers))
	seenAddresses := make(map[string]struct{}, len(inbound.Peers))
	serverPrefix, _ := netip.ParsePrefix(serverCIDR)
	for _, peer := range inbound.Peers {
		if err := validateWireGuardKey(strings.TrimSpace(peer.PublicKey), "peer public_key", false); err != nil {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q user %d device %d: %w", tag, peer.UserID, peer.DeviceIndex, err)
		}
		if _, exists := seenKeys[peer.PublicKey]; exists {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: duplicate peer public key", tag)
		}
		seenKeys[peer.PublicKey] = struct{}{}
		addr, parseErr := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if parseErr != nil || !addr.Is4() || !wireGuardUsableAddress(pool, addr) {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q user %d device %d: invalid peer address %q", tag, peer.UserID, peer.DeviceIndex, peer.Address)
		}
		if addr == serverPrefix.Addr() {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: peer address conflicts with server address", tag)
		}
		if _, exists := seenAddresses[addr.String()]; exists {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: duplicate peer address %s", tag, addr)
		}
		seenAddresses[addr.String()] = struct{}{}
		if err := validateWireGuardKey(strings.TrimSpace(peer.PresharedKey), "peer preshared_key", true); err != nil {
			return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q user %d device %d: %w", tag, peer.UserID, peer.DeviceIndex, err)
		}
	}
	interfaceName, err := amneziaWGGeneratedInterfaceName(tag)
	if err != nil {
		return preparedAmneziaWGRuntime{}, err
	}
	configText := renderAmneziaWGAuditConfig(inbound, serverCIDR, mtu, obfs)
	dir := filepath.Join(s.cfg.DataDir, "amneziawg", "runtime", amneziaWGRuntimeDirName(tag))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: create runtime directory: %w", tag, err)
	}
	path := filepath.Join(dir, "interface.conf")
	if err := atomicWriteFile(path, []byte(configText), 0600); err != nil {
		return preparedAmneziaWGRuntime{}, fmt.Errorf("amneziawg %q: write runtime config: %w", tag, err)
	}
	return preparedAmneziaWGRuntime{Tag: tag, InterfaceName: interfaceName, ConfigPath: path, ConfigText: configText, ServerCIDR: serverCIDR, SourceCIDR: pool.String(), MTU: mtu, Obfuscation: obfs, Inbound: inbound}, nil
}

func amneziaWGRuntimeAddressing(settings map[string]any) (netip.Prefix, string, error) {
	rawPool := wireGuardStringSetting(settings, "address_pool")
	if rawPool == "" {
		rawPool = defaultAmneziaWGPoolCIDR
	}
	pool, err := netip.ParsePrefix(rawPool)
	if err != nil || !pool.Addr().Is4() {
		return netip.Prefix{}, "", fmt.Errorf("invalid IPv4 address pool %q", rawPool)
	}
	pool = pool.Masked()
	server := wireGuardStringSetting(settings, "server_address")
	if server == "" {
		server = pool.Addr().Next().String() + "/" + strconv.Itoa(pool.Bits())
	}
	serverPrefix, err := netip.ParsePrefix(server)
	if err != nil || !serverPrefix.Addr().Is4() || !pool.Contains(serverPrefix.Addr()) {
		return netip.Prefix{}, "", fmt.Errorf("invalid server address %q for pool %s", server, pool)
	}
	return pool, serverPrefix.String(), nil
}

func amneziaWGObfuscationSettings(settings map[string]any) (amneziaWGObfuscation, error) {
	obfs := amneziaWGObfuscation{Jc: 4, Jmin: 8, Jmax: 80, S1: 77, S2: 90}
	for key, target := range map[string]*int{"jc": &obfs.Jc, "jmin": &obfs.Jmin, "jmax": &obfs.Jmax, "s1": &obfs.S1, "s2": &obfs.S2} {
		if value := wireGuardIntSetting(settings, key); value != 0 {
			*target = value
		}
	}
	obfs.H1 = wireGuardStringSetting(settings, "h1")
	obfs.H2 = wireGuardStringSetting(settings, "h2")
	obfs.H3 = wireGuardStringSetting(settings, "h3")
	obfs.H4 = wireGuardStringSetting(settings, "h4")
	if obfs.Jc < 0 || obfs.Jc > 128 || obfs.Jmin < 0 || obfs.Jmin > obfs.Jmax || obfs.Jmax > 128 || obfs.S1 < 0 || obfs.S1 > 255 || obfs.S2 < 0 || obfs.S2 > 255 {
		return amneziaWGObfuscation{}, fmt.Errorf("invalid AWG obfuscation numeric parameters")
	}
	headers := []string{obfs.H1, obfs.H2, obfs.H3, obfs.H4}
	seen := map[string]struct{}{}
	for _, header := range headers {
		value, err := strconv.ParseUint(header, 10, 32)
		if err != nil || value < 4 {
			return amneziaWGObfuscation{}, fmt.Errorf("H1-H4 must be distinct decimal values greater than 3")
		}
		if _, exists := seen[header]; exists {
			return amneziaWGObfuscation{}, fmt.Errorf("H1-H4 must be distinct decimal values greater than 3")
		}
		seen[header] = struct{}{}
	}
	return obfs, nil
}

func renderAmneziaWGAuditConfig(inbound amneziaWGRuntimeInbound, serverCIDR string, mtu int, obfs amneziaWGObfuscation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nListenPort = %d\nMTU = %d\n", wireGuardStringSetting(inbound.Settings, "private_key"), serverCIDR, inbound.ListenPort, mtu)
	fmt.Fprintf(&b, "Jc = %d\nJmin = %d\nJmax = %d\nS1 = %d\nS2 = %d\nH1 = %s\nH2 = %s\nH3 = %s\nH4 = %s\n", obfs.Jc, obfs.Jmin, obfs.Jmax, obfs.S1, obfs.S2, obfs.H1, obfs.H2, obfs.H3, obfs.H4)
	peers := append([]amneziaWGRuntimePeer(nil), inbound.Peers...)
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].UserID == peers[j].UserID {
			return peers[i].DeviceIndex < peers[j].DeviceIndex
		}
		return peers[i].UserID < peers[j].UserID
	})
	for _, peer := range peers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", peer.PublicKey)
		if peer.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", peer.PresharedKey)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s/32\n", peer.Address)
	}
	return b.String()
}

func amneziaWGRuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *Server) preflightAmneziaWGRuntimes(prepared []preparedAmneziaWGRuntime) error {
	if len(prepared) == 0 {
		return nil
	}
	return amneziaWGPlatformPreflight()
}

func (s *Server) applyAmneziaWGRuntime(prepared preparedAmneziaWGRuntime) error {
	if err := amneziaWGPlatformApply(prepared); err != nil {
		return fmt.Errorf("amneziawg %q: %w", prepared.Tag, err)
	}
	s.mu.Lock()
	s.amneziaWGRuntimes[prepared.Tag] = amneziaWGRuntimeState{
		Tag: prepared.Tag, InterfaceName: prepared.InterfaceName, ConfigPath: prepared.ConfigPath,
		ServerCIDR: prepared.ServerCIDR, SourceCIDR: prepared.SourceCIDR, MTU: prepared.MTU,
	}
	s.mu.Unlock()
	s.appendLog(fmt.Sprintf("amneziawg runtime applied: tag=%s interface=%s listen=%d peers=%d", prepared.Tag, prepared.InterfaceName, prepared.Inbound.ListenPort, len(prepared.Inbound.Peers)))
	return nil
}

func (s *Server) stopRemovedAmneziaWGRuntimes(desired map[string]preparedAmneziaWGRuntime) {
	s.mu.Lock()
	states := make(map[string]amneziaWGRuntimeState, len(s.amneziaWGRuntimes))
	for tag, state := range s.amneziaWGRuntimes {
		states[tag] = state
	}
	s.mu.Unlock()
	for tag, state := range states {
		if _, keep := desired[tag]; keep {
			continue
		}
		if err := amneziaWGPlatformRemove(state.InterfaceName); err != nil {
			s.appendLog("remove amneziawg interface failed: " + err.Error())
			continue
		}
		s.mu.Lock()
		delete(s.amneziaWGRuntimes, tag)
		s.mu.Unlock()
	}
}

func (s *Server) stopAllAmneziaWGRuntimes() {
	s.stopRemovedAmneziaWGRuntimes(map[string]preparedAmneziaWGRuntime{})
}
