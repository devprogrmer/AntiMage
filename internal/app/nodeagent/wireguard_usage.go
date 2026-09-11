package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type wireGuardRuntimeInbound struct {
	Tag        string                 `json:"tag"`
	TunnelTag  string                 `json:"tunnel_tag"`
	ListenPort int                    `json:"listen_port"`
	TunnelPort int                    `json:"tunnel_port"`
	Settings   map[string]any         `json:"settings"`
	Peers      []wireGuardRuntimePeer `json:"peers"`
}

type wireGuardRuntimePeer struct {
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	PublicKey    string `json:"public_key"`
	PresharedKey string `json:"preshared_key,omitempty"`
	Address      string `json:"address"`
	Status       string `json:"status"`
	UsedTraffic  int64  `json:"used_traffic"`
	DataLimit    *int64 `json:"data_limit,omitempty"`
	Expire       *int64 `json:"expire,omitempty"`
	DeviceLimit  int64  `json:"device_limit,omitempty"`
}

type wireGuardUsageRuntimeConfig struct {
	InboundTag    string           `json:"inbound_tag"`
	InterfaceName string           `json:"interface_name,omitempty"`
	ListenPort    int              `json:"listen_port"`
	Peers         map[string]int64 `json:"peers"`
}

type wireGuardPeerCounters struct {
	PublicKey       string
	LatestHandshake int64
	ReceivedBytes   uint64
	SentBytes       uint64
}

var wireGuardInterfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

var wireGuardDumpInterface = func(
	ctx context.Context,
	interfaceName string,
) ([]byte, error) {
	path, err := exec.LookPath("wg")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(
		ctx,
		path,
		"show",
		interfaceName,
		"dump",
	).Output()
}

var wireGuardDumpAll = func(
	ctx context.Context,
) ([]byte, error) {
	path, err := exec.LookPath("wg")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(
		ctx,
		path,
		"show",
		"all",
		"dump",
	).Output()
}

type wireGuardInterfaceDump struct {
	Name       string
	ListenPort int
	Peers      []wireGuardPeerCounters
}

func parseWireGuardDump(raw string) ([]wireGuardPeerCounters, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	result := make([]wireGuardPeerCounters, 0)

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 4 {
			continue
		}
		if len(parts) < 8 {
			return nil, fmt.Errorf(
				"wireguard dump row has %d fields, want at least 8",
				len(parts),
			)
		}

		publicKey := strings.TrimSpace(parts[0])
		if publicKey == "" {
			continue
		}
		handshake, err := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q latest handshake: %w",
				publicKey,
				err,
			)
		}
		received, err := strconv.ParseUint(strings.TrimSpace(parts[5]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q received bytes: %w",
				publicKey,
				err,
			)
		}
		sent, err := strconv.ParseUint(strings.TrimSpace(parts[6]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q sent bytes: %w",
				publicKey,
				err,
			)
		}
		result = append(result, wireGuardPeerCounters{
			PublicKey:       publicKey,
			LatestHandshake: handshake,
			ReceivedBytes:   received,
			SentBytes:       sent,
		})
	}
	return result, nil
}

func parseWireGuardAllDump(
	raw string,
) (map[string]wireGuardInterfaceDump, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	result := make(map[string]wireGuardInterfaceDump)

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 5 {
			name := strings.TrimSpace(parts[0])
			if name == "" {
				continue
			}
			listenPort, err := strconv.Atoi(strings.TrimSpace(parts[3]))
			if err != nil {
				return nil, fmt.Errorf(
					"wireguard interface %q listen port: %w",
					name,
					err,
				)
			}
			result[name] = wireGuardInterfaceDump{
				Name:       name,
				ListenPort: listenPort,
				Peers:      []wireGuardPeerCounters{},
			}
			continue
		}
		if len(parts) < 9 {
			return nil, fmt.Errorf(
				"wireguard all-dump row has %d fields, want 5 or at least 9",
				len(parts),
			)
		}

		name := strings.TrimSpace(parts[0])
		publicKey := strings.TrimSpace(parts[1])
		if name == "" || publicKey == "" {
			continue
		}

		handshake, err := strconv.ParseInt(strings.TrimSpace(parts[5]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q latest handshake: %w",
				publicKey,
				err,
			)
		}
		received, err := strconv.ParseUint(strings.TrimSpace(parts[6]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q received bytes: %w",
				publicKey,
				err,
			)
		}
		sent, err := strconv.ParseUint(strings.TrimSpace(parts[7]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"wireguard peer %q sent bytes: %w",
				publicKey,
				err,
			)
		}

		item, ok := result[name]
		if !ok {
			return nil, fmt.Errorf(
				"wireguard peer %q references unknown interface %q",
				publicKey,
				name,
			)
		}
		item.Peers = append(item.Peers, wireGuardPeerCounters{
			PublicKey:       publicKey,
			LatestHandshake: handshake,
			ReceivedBytes:   received,
			SentBytes:       sent,
		})
		result[name] = item
	}

	return result, nil
}

func wireGuardPeerTotalBytes(peer wireGuardPeerCounters) (uint64, error) {
	if ^uint64(0)-peer.ReceivedBytes < peer.SentBytes {
		return 0, fmt.Errorf("wireguard byte counter overflow")
	}
	return peer.ReceivedBytes + peer.SentBytes, nil
}

func wireGuardUsageBaselineKey(
	inboundTag string,
	interfaceName string,
	publicKey string,
) string {
	return strings.Join([]string{
		strings.TrimSpace(inboundTag),
		strings.TrimSpace(interfaceName),
		strings.TrimSpace(publicKey),
	}, "\x00")
}

func wireGuardRuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func wireGuardInterfaceName(inbound wireGuardRuntimeInbound) (string, error) {
	for _, key := range []string{"interface_name", "interface", "device"} {
		if value := wireGuardStringSetting(inbound.Settings, key); value != "" {
			if !wireGuardInterfaceNamePattern.MatchString(value) {
				return "", fmt.Errorf(
					"wireguard %q: invalid interface name %q",
					inbound.Tag,
					value,
				)
			}
			return value, nil
		}
	}
	return "", nil
}

func wireGuardStringSetting(settings map[string]any, key string) string {
	value, ok := settings[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func wireGuardBoolSetting(
	settings map[string]any,
	key string,
	fallback bool,
) bool {
	value, ok := settings[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		default:
			return fallback
		}
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return fallback
	}
}

func (s *Server) syncWireGuardUsageConfigs(
	inbounds []wireGuardRuntimeInbound,
) error {
	root := filepath.Join(s.cfg.DataDir, "wireguard", "inbounds")
	if err := os.MkdirAll(root, 0700); err != nil {
		return fmt.Errorf("create wireguard usage config root: %w", err)
	}

	desiredDirs := make(map[string]struct{})
	interfaceOwners := make(map[string]string)

	for _, inbound := range inbounds {
		tag := strings.TrimSpace(inbound.Tag)
		if tag == "" {
			return fmt.Errorf("wireguard inbound tag is required")
		}
		if !wireGuardBoolSetting(
			inbound.Settings,
			"accounting_enabled",
			true,
		) {
			continue
		}

		interfaceName, err := wireGuardInterfaceName(inbound)
		if err != nil {
			return err
		}
		if owner, exists := interfaceOwners[interfaceName]; exists && owner != tag {
			return fmt.Errorf(
				"wireguard interface %q is assigned to both %q and %q",
				interfaceName,
				owner,
				tag,
			)
		}
		interfaceOwners[interfaceName] = tag

		peers := make(map[string]int64, len(inbound.Peers))
		for _, peer := range inbound.Peers {
			publicKey := strings.TrimSpace(peer.PublicKey)
			if publicKey == "" || peer.UserID <= 0 {
				return fmt.Errorf(
					"wireguard %q: invalid peer mapping for user %d",
					tag,
					peer.UserID,
				)
			}
			if _, exists := peers[publicKey]; exists {
				return fmt.Errorf(
					"wireguard %q: duplicate peer public key %q",
					tag,
					publicKey,
				)
			}
			peers[publicKey] = peer.UserID
		}

		dirName := wireGuardRuntimeDirName(tag)
		desiredDirs[dirName] = struct{}{}
		dir := filepath.Join(root, dirName)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create wireguard usage config dir: %w", err)
		}

		raw, err := json.Marshal(wireGuardUsageRuntimeConfig{
			InboundTag:    tag,
			InterfaceName: interfaceName,
			ListenPort:    inbound.ListenPort,
			Peers:         peers,
		})
		if err != nil {
			return fmt.Errorf(
				"wireguard %q: marshal usage config: %w",
				tag,
				err,
			)
		}
		path := filepath.Join(dir, "usage-helper.json")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0600); err != nil {
			return fmt.Errorf(
				"wireguard %q: write usage config: %w",
				tag,
				err,
			)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf(
				"wireguard %q: replace usage config: %w",
				tag,
				err,
			)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("list wireguard usage config root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, keep := desiredDirs[entry.Name()]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf(
				"remove stale wireguard usage config: %w",
				err,
			)
		}
	}
	return nil
}
