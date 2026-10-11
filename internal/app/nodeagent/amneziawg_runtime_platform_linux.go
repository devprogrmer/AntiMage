//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	wgctrl "github.com/Jipok/wgctrl-go"
	"github.com/Jipok/wgctrl-go/wgtypes"
	"github.com/vishvananda/netlink"
)

func amneziaWGPlatformInterfaceIdentity(name string) (string, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(string(boot)), link.Attrs().Index), nil
}

func amneziaWGDurableWrite(path string, raw []byte) error {
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func amneziaWGPlatformSnapshotAll(ctx context.Context, names []string) (map[string][]wireGuardPeerCounters, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	result := make(map[string][]wireGuardPeerCounters, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		device, err := client.Device(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("AWG snapshot %s: %w", name, err)
		}
		peers := make([]wireGuardPeerCounters, 0, len(device.Peers))
		for _, peer := range device.Peers {
			endpoint := ""
			if peer.Endpoint != nil {
				endpoint = peer.Endpoint.String()
			}
			peers = append(peers, wireGuardPeerCounters{PublicKey: peer.PublicKey.String(), Endpoint: endpoint, LatestHandshake: peer.LastHandshakeTime.Unix(), ReceivedBytes: uint64(max(peer.ReceiveBytes, 0)), SentBytes: uint64(max(peer.TransmitBytes, 0))})
		}
		result[name] = peers
	}
	return result, nil
}

func amneziaWGPlatformPreflight() error {
	if _, err := exec.LookPath("modprobe"); err != nil {
		return fmt.Errorf("modprobe is not installed")
	}
	return nil
}

func amneziaWGPlatformApply(prepared preparedAmneziaWGRuntime) error {
	if err := ensureAmneziaWGProvisioned(); err != nil {
		return err
	}
	link, err := netlink.LinkByName(prepared.InterfaceName)
	if err != nil {
		attrs := netlink.NewLinkAttrs()
		attrs.Name = prepared.InterfaceName
		link = &netlink.GenericLink{LinkAttrs: attrs, LinkType: "amneziawg"}
		if err := netlink.LinkAdd(link); err != nil {
			return fmt.Errorf("create kernel interface: %w", err)
		}
		link, err = netlink.LinkByName(prepared.InterfaceName)
		if err != nil {
			return fmt.Errorf("read created interface: %w", err)
		}
	}
	privateKey, err := wgtypes.ParseKey(wireGuardStringSetting(prepared.Inbound.Settings, "private_key"))
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	listenPort := prepared.Inbound.ListenPort
	obfs := prepared.Obfuscation
	cfg := wgtypes.Config{PrivateKey: &privateKey, ListenPort: &listenPort, ReplacePeers: true,
		Jc: &obfs.Jc, Jmin: &obfs.Jmin, Jmax: &obfs.Jmax, S1: &obfs.S1, S2: &obfs.S2,
		H1: &obfs.H1, H2: &obfs.H2, H3: &obfs.H3, H4: &obfs.H4}
	for _, peer := range prepared.Inbound.Peers {
		publicKey, parseErr := wgtypes.ParseKey(peer.PublicKey)
		if parseErr != nil {
			return parseErr
		}
		_, subnet, parseErr := net.ParseCIDR(peer.Address + "/32")
		if parseErr != nil {
			return parseErr
		}
		peerCfg := wgtypes.PeerConfig{PublicKey: publicKey, ReplaceAllowedIPs: true, AllowedIPs: []net.IPNet{*subnet}}
		if strings.TrimSpace(peer.PresharedKey) != "" {
			psk, pskErr := wgtypes.ParseKey(peer.PresharedKey)
			if pskErr != nil {
				return pskErr
			}
			peerCfg.PresharedKey = &psk
		}
		cfg.Peers = append(cfg.Peers, peerCfg)
	}
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("open AmneziaWG netlink client: %w", err)
	}
	defer client.Close()
	if err := client.ConfigureDevice(prepared.InterfaceName, cfg); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list addresses: %w", err)
	}
	for _, addr := range addresses {
		if err := netlink.AddrDel(link, &addr); err != nil {
			return err
		}
	}
	addr, err := netlink.ParseAddr(prepared.ServerCIDR)
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("assign address: %w", err)
	}
	if err := netlink.LinkSetMTU(link, prepared.MTU); err != nil {
		return fmt.Errorf("set MTU: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring interface up: %w", err)
	}
	return nil
}

func amneziaWGPlatformRemove(interfaceName string) error {
	link, err := netlink.LinkByName(interfaceName)
	if err != nil {
		return nil
	}
	return netlink.LinkDel(link)
}

func amneziaWGPlatformSnapshot(interfaceName string) ([]wireGuardPeerCounters, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	device, err := client.Device(interfaceName)
	if err != nil {
		return nil, err
	}
	result := make([]wireGuardPeerCounters, 0, len(device.Peers))
	for _, peer := range device.Peers {
		endpoint := ""
		if peer.Endpoint != nil {
			endpoint = peer.Endpoint.String()
		}
		result = append(result, wireGuardPeerCounters{PublicKey: peer.PublicKey.String(), Endpoint: endpoint, LatestHandshake: peer.LastHandshakeTime.Unix(), ReceivedBytes: uint64(max(peer.ReceiveBytes, 0)), SentBytes: uint64(max(peer.TransmitBytes, 0))})
	}
	return result, nil
}

func amneziaWGPlatformRemovePeer(interfaceName, publicKey string) error {
	key, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		return err
	}
	client, err := wgctrl.New()
	if err != nil {
		return err
	}
	defer client.Close()
	return client.ConfigureDevice(interfaceName, wgtypes.Config{Peers: []wgtypes.PeerConfig{{PublicKey: key, Remove: true}}})
}
