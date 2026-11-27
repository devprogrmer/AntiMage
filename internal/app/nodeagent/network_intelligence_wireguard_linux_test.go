//go:build linux

package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This fixture owns its namespace and interface names. Run only in a disposable
// container with NET_ADMIN and SYS_ADMIN, never in a production namespace.
func TestNetworkIntelligenceRealWireGuardPublication(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Skip("requires disposable Linux namespace fixture")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		if _, guestErr := os.Stat("/run/antimage-disposable-network-lab"); guestErr != nil {
			t.Fatal("fixture requires a disposable Docker container or explicit disposable guest")
		}
	}
	for _, name := range []string{"ni-root", "ni-server", "ni-server2"} {
		if _, err := net.InterfaceByName(name); err == nil {
			t.Fatal("fixture interface already exists")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := func(input, command string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture command %s failed: %v", command, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "ip", "netns", "add", "ni-client")
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "del", "ni-client").Run()
		_ = exec.Command("ip", "link", "del", "ni-root").Run()
		_ = exec.Command("ip", "link", "del", "ni-server").Run()
	})
	run("", "ip", "link", "add", "ni-root", "type", "veth", "peer", "name", "ni-peer")
	run("", "ip", "link", "set", "ni-peer", "netns", "ni-client")
	run("", "ip", "addr", "add", "172.30.211.1/30", "dev", "ni-root")
	run("", "ip", "link", "set", "ni-root", "up")
	run("", "ip", "-n", "ni-client", "addr", "add", "172.30.211.2/30", "dev", "ni-peer")
	run("", "ip", "-n", "ni-client", "link", "set", "ni-peer", "up")
	run("", "ip", "-n", "ni-client", "link", "set", "lo", "up")
	serverKey := run("", "wg", "genkey")
	serverPublic := run(serverKey, "wg", "pubkey")
	run("", "ip", "link", "add", "ni-server", "type", "wireguard")
	run(serverKey, "wg", "set", "ni-server", "private-key", "/dev/stdin", "listen-port", "59171")
	run("", "ip", "addr", "add", "10.123.211.1/24", "dev", "ni-server")
	run("", "ip", "link", "set", "ni-server", "up")
	run("", "ip", "link", "set", "ni-server", "alias", wireGuardOwnershipAlias("fixture"))
	run("", "ip", "-n", "ni-client", "link", "add", "ni-wg", "type", "wireguard")
	run("", "ip", "-n", "ni-client", "addr", "add", "10.123.211.2/24", "dev", "ni-wg")
	run("", "ip", "-n", "ni-client", "link", "set", "ni-wg", "up")
	udp, err := net.ListenPacket("udp4", "10.123.211.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	tcp, err := net.Listen("tcp4", "10.123.211.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	s := New(Config{DataDir: t.TempDir(), NetworkIntelligenceEnabled: true})
	defer s.stopNetworkIntelligence()
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"target":"node:7","session_callback":{"node_id":7}}`}); err != nil {
		t.Fatal(err)
	}
	tracker := s.networkIntelligenceTracker
	var oldPeer string
	for generation := uint64(1); generation <= 2; generation++ {
		private := run("", "wg", "genkey")
		public := run(private, "wg", "pubkey")
		err := s.withNetworkIntelligenceMutation("ni-server", func() error {
			if oldPeer != "" {
				run("", "wg", "set", "ni-server", "peer", oldPeer, "remove")
			}
			run("", "wg", "set", "ni-server", "peer", public, "allowed-ips", "10.123.211.2/32")
			run(private, "ip", "netns", "exec", "ni-client", "wg", "set", "ni-wg", "private-key", "/dev/stdin", "peer", serverPublic, "allowed-ips", "10.123.211.1/32", "endpoint", "172.30.211.1:59171")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tracker.Snapshot(time.Now())) != 0 {
			t.Fatal("previous owner flows survived mutation")
		}
		prepared := preparedWireGuardRuntime{Tag: "fixture", InterfaceName: "ni-server", Inbound: wireGuardRuntimeInbound{Peers: []wireGuardRuntimePeer{{UserID: int64(40 + generation), PublicKey: public, Address: "10.123.211.2"}}}}
		s.networkIntelligenceMu.Lock()
		err = s.publishNetworkIntelligence(prepared)
		s.networkIntelligenceMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		code := "import socket; u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); u.sendto(b'fixture',('10.123.211.1'," + strings.Split(udp.LocalAddr().String(), ":")[1] + ")); s=socket.create_connection(('10.123.211.1'," + strings.Split(tcp.Addr().String(), ":")[1] + "),5); s.sendall(b'fixture'); s.close()"
		run("", "ip", "netns", "exec", "ni-client", "python3", "-c", code)
		connection, err := tcp.Accept()
		if err != nil {
			t.Fatal(err)
		}
		connection.Close()
		deadline := time.Now().Add(2 * time.Second)
		for {
			foundTCP, foundUDP := false, false
			for _, flow := range tracker.Snapshot(time.Now()) {
				if flow.Binding.UserID != int64(40+generation) || flow.Binding.NodeID != 7 || flow.Binding.Generation != generation {
					t.Fatal("flow attributed to wrong peer generation")
				}
				foundTCP = foundTCP || flow.Tuple.Protocol == "tcp"
				foundUDP = foundUDP || flow.Tuple.Protocol == "udp"
			}
			if foundTCP && foundUDP {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("missing real encrypted TCP or UDP flow")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if run("", "wg", "show", "ni-server", "latest-handshakes") == public+"\t0" {
			t.Fatal("WireGuard handshake absent")
		}
		oldPeer = public
	}
	// A second managed interface and peer must resolve to the same account
	// while retaining separate interface and assigned-address identities.
	private := run("", "wg", "genkey")
	public := run(private, "wg", "pubkey")
	run("", "ip", "link", "add", "ni-server2", "type", "wireguard")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", "ni-server2").Run() })
	run(serverKey, "wg", "set", "ni-server2", "private-key", "/dev/stdin", "listen-port", "59172", "peer", public, "allowed-ips", "10.124.211.2/32")
	run("", "ip", "addr", "add", "10.124.211.1/24", "dev", "ni-server2")
	run("", "ip", "link", "set", "ni-server2", "alias", wireGuardOwnershipAlias("fixture2"))
	run("", "ip", "link", "set", "ni-server2", "up")
	run("", "ip", "-n", "ni-client", "link", "add", "ni-wg2", "type", "wireguard")
	run("", "ip", "-n", "ni-client", "addr", "add", "10.124.211.2/24", "dev", "ni-wg2")
	run(private, "ip", "netns", "exec", "ni-client", "wg", "set", "ni-wg2", "private-key", "/dev/stdin", "peer", serverPublic, "allowed-ips", "10.124.211.1/32", "endpoint", "172.30.211.1:59172")
	run("", "ip", "-n", "ni-client", "link", "set", "ni-wg2", "up")
	secondUDP, err := net.ListenPacket("udp4", "10.124.211.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer secondUDP.Close()
	prepared := preparedWireGuardRuntime{Tag: "fixture2", InterfaceName: "ni-server2", Inbound: wireGuardRuntimeInbound{Peers: []wireGuardRuntimePeer{{UserID: 42, PublicKey: public, Address: "10.124.211.2"}}}}
	s.networkIntelligenceMu.Lock()
	err = s.publishNetworkIntelligence(prepared)
	s.networkIntelligenceMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run("", "ip", "netns", "exec", "ni-client", "python3", "-c", "import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.sendto(b'fixture',('10.124.211.1',"+strings.Split(secondUDP.LocalAddr().String(), ":")[1]+"))")
	deadline := time.Now().Add(2 * time.Second)
	for {
		interfaces := map[string]bool{}
		for _, flow := range tracker.Snapshot(time.Now()) {
			if flow.Binding.UserID != 42 {
				t.Fatal("multi-interface account identity changed")
			}
			interfaces[flow.Binding.Interface] = true
		}
		if interfaces["ni-server"] && interfaces["ni-server2"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing second real WireGuard interface flow")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
