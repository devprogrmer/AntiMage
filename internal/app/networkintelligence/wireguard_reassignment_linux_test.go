//go:build linux

package networkintelligence

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This fixture owns its namespace and interface names. Run only in a disposable
// container with NET_ADMIN and SYS_ADMIN, never in a production namespace.
func TestWireGuardRealPeerReassignment(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Skip("requires disposable Linux namespace fixture")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		if _, guestErr := os.Stat("/run/antimage-disposable-network-lab"); guestErr != nil {
			t.Fatal("fixture requires a disposable Docker container or explicit disposable guest")
		}
	}
	for _, name := range []string{"ni-root", "ni-server"} {
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
			if command == "ip" {
				t.Logf("namespace command output: %s", strings.TrimSpace(string(out)))
			}
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
	tracker, _ := New(128, time.Minute)
	lifecycle, _ := NewLifecycle(tracker, 1)
	lifecycle.SetEnabled(true)
	defer lifecycle.SetEnabled(false)
	var oldPeer string
	for generation := uint64(1); generation <= 2; generation++ {
		private := run("", "wg", "genkey")
		public := run(private, "wg", "pubkey")
		err := lifecycle.Mutate("ni-server", func() error {
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
		binding := Binding{NodeID: 7, Interface: "ni-server", Address: netip.MustParseAddr("10.123.211.2"), UserID: int64(40 + generation), Session: public, Generation: generation, Since: time.Now()}
		err = lifecycle.Publish(ctx, CaptureConfig{Interface: "ni-server"}, []Binding{binding}, func() (CaptureSession, error) { return NewSessionCapture(tracker, []Binding{binding}, 128) })
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
				if flow.Binding != binding {
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
}
