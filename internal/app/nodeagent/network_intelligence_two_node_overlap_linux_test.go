//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// This test creates two isolated WireGuard nodes and two client namespaces.
// Both clients use the same tunnel address, but belong to different node/user
// identities. It must run only in the disposable Linux guest used by CI.
func TestNetworkIntelligenceRealWireGuardOverlappingAddressAcrossNodes(t *testing.T) {
	if os.Getenv("ANTIMAGE_WIREGUARD_NI_E2E") != "1" {
		t.Skip("requires disposable Linux namespace fixture")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		if _, guestErr := os.Stat("/run/antimage-disposable-network-lab"); guestErr != nil {
			t.Fatal("fixture requires a disposable Docker container or explicit disposable guest")
		}
	}
	names := []string{"nira", "nirb", "nina", "ninb"}
	for _, name := range names {
		if _, err := net.InterfaceByName(name); err == nil {
			t.Fatalf("fixture interface %q already exists", name)
		}
	}
	for _, namespace := range []string{"ni-client-overlap-a", "ni-client-overlap-b"} {
		if err := exec.Command("ip", "netns", "exec", namespace, "true").Run(); err == nil {
			t.Fatalf("fixture namespace %q already exists", namespace)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	run := func(input, command string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture command %s %s failed: %v: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out))
	}
	for _, suffix := range []string{"a", "b"} {
		namespace := "ni-client-overlap-" + suffix
		rootIf, peerIf := "nir"+suffix, "nip"+suffix
		underlay := "172.31." + map[string]string{"a": "41", "b": "42"}[suffix]
		run("", "ip", "netns", "add", namespace)
		run("", "ip", "link", "add", rootIf, "type", "veth", "peer", "name", peerIf)
		run("", "ip", "link", "set", peerIf, "netns", namespace)
		run("", "ip", "addr", "add", underlay+".1/30", "dev", rootIf)
		run("", "ip", "link", "set", rootIf, "up")
		run("", "ip", "-n", namespace, "addr", "add", underlay+".2/30", "dev", peerIf)
		run("", "ip", "-n", namespace, "link", "set", peerIf, "up")
		run("", "ip", "-n", namespace, "link", "set", "lo", "up")
	}
	t.Cleanup(func() {
		for _, namespace := range []string{"ni-client-overlap-a", "ni-client-overlap-b"} {
			_ = exec.Command("ip", "netns", "del", namespace).Run()
		}
		for _, name := range names {
			_ = exec.Command("ip", "link", "del", name).Run()
		}
	})

	type nodeFixture struct {
		server       *Server
		nodeID       int64
		userID       int64
		iface        string
		namespace    string
		destination  string
		underlayPeer string
		listenPort   string
		udp          *net.UDPConn
		tcp          net.Listener
	}
	newNode := func(suffix string, nodeID, userID int64, destination, underlayPeer, listenPort string) nodeFixture {
		t.Helper()
		iface := "ni" + "n" + suffix
		namespace := "ni-client-overlap-" + suffix
		serverPrivate := run("", "wg", "genkey")
		serverPublic := run(serverPrivate, "wg", "pubkey")
		clientPrivate := run("", "wg", "genkey")
		clientPublic := run(clientPrivate, "wg", "pubkey")
		run("", "ip", "link", "add", iface, "type", "wireguard")
		run(serverPrivate, "wg", "set", iface, "private-key", "/dev/stdin", "listen-port", listenPort,
			"peer", clientPublic, "allowed-ips", "10.99.99.2/32")
		run("", "ip", "addr", "add", destination+"/24", "dev", iface)
		run("", "ip", "link", "set", iface, "alias", wireGuardOwnershipAlias("overlap-"+suffix))
		run("", "ip", "link", "set", iface, "up")
		clientIface := "niw" + suffix
		run("", "ip", "-n", namespace, "link", "add", clientIface, "type", "wireguard")
		run("", "ip", "-n", namespace, "addr", "add", "10.99.99.2/32", "dev", clientIface)
		run("", "ip", "-n", namespace, "link", "set", clientIface, "up")
		run(clientPrivate, "ip", "netns", "exec", namespace, "wg", "set", clientIface,
			"private-key", "/dev/stdin", "peer", serverPublic, "allowed-ips", destination+"/32",
			"endpoint", underlayPeer+":"+listenPort)
		run("", "ip", "-n", namespace, "route", "add", destination+"/32", "dev", clientIface)
		// The fixture places both simulated nodes in the disposable host
		// namespace. Source-based tables keep each local service reply on the
		// WireGuard interface that received the overlapping tunnel address.
		table := map[string]string{"a": "201", "b": "202"}[suffix]
		run("", "ip", "rule", "add", "from", destination+"/32", "table", table)
		run("", "ip", "route", "add", "10.99.99.0/24", "dev", iface, "table", table)

		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(destination), Port: 39111})
		if err != nil {
			t.Fatal(err)
		}
		tcp, err := net.Listen("tcp4", net.JoinHostPort(destination, "39112"))
		if err != nil {
			udp.Close()
			t.Fatal(err)
		}
		server := New(Config{DataDir: t.TempDir(), NetworkIntelligenceEnabled: true})
		t.Cleanup(server.stopNetworkIntelligence)
		policy := fmt.Sprintf(`{"target":"node:%d","session_callback":{"node_id":%d}}`, nodeID, nodeID)
		if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: policy}); err != nil {
			t.Fatal(err)
		}
		prepared := preparedWireGuardRuntime{Tag: "overlap-" + suffix, InterfaceName: iface,
			Inbound: wireGuardRuntimeInbound{Peers: []wireGuardRuntimePeer{{UserID: userID, PublicKey: clientPublic, Address: "10.99.99.2"}}}}
		server.networkIntelligenceMu.Lock()
		err = server.publishNetworkIntelligence(prepared)
		server.networkIntelligenceMu.Unlock()
		if err != nil {
			udp.Close()
			tcp.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close() })
		return nodeFixture{server: server, nodeID: nodeID, userID: userID, iface: iface, namespace: namespace, destination: destination, underlayPeer: underlayPeer, listenPort: listenPort, udp: udp, tcp: tcp}
	}
	nodes := []nodeFixture{
		newNode("a", 701, 41, "10.201.99.1", "172.31.41.1", "59301"),
		newNode("b", 702, 42, "10.202.99.1", "172.31.42.1", "59302"),
	}
	if nodes[0].server.networkIntelligenceTracker == nil || nodes[1].server.networkIntelligenceTracker == nil {
		t.Fatal("both isolated node agents must initialize a collector")
	}
	if nodes[0].userID == nodes[1].userID || nodes[0].nodeID == nodes[1].nodeID || nodes[0].destination == nodes[1].destination {
		t.Fatal("fixture must use distinct account/node identities and destinations")
	}
	if nodes[0].server.networkIntelligenceTracker == nodes[1].server.networkIntelligenceTracker {
		t.Fatal("isolated node agents unexpectedly share a flow tracker")
	}
	for _, node := range nodes {
		udpResult := make(chan error, 1)
		go func(udp *net.UDPConn) {
			_ = udp.SetReadDeadline(time.Now().Add(5 * time.Second))
			buffer := make([]byte, 128)
			n, peer, err := udp.ReadFromUDP(buffer)
			if err == nil && string(buffer[:n]) != "overlap-udp-request" {
				err = fmt.Errorf("unexpected UDP request %q", string(buffer[:n]))
			}
			if err == nil {
				_, err = udp.WriteToUDP([]byte("overlap-udp-reply"), peer)
			}
			udpResult <- err
		}(node.udp)
		tcpResult := make(chan error, 1)
		go func(listener net.Listener) {
			_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
			conn, err := listener.Accept()
			if err == nil {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				buffer := make([]byte, 128)
				var n int
				n, err = conn.Read(buffer)
				if err == nil && string(buffer[:n]) != "overlap-tcp-request" {
					err = fmt.Errorf("unexpected TCP request %q", string(buffer[:n]))
				}
				if err == nil {
					_, err = conn.Write([]byte("overlap-tcp-reply"))
				}
			}
			tcpResult <- err
		}(node.tcp)
		python := "import socket; u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); u.bind(('10.99.99.2',42000)); u.settimeout(4); u.sendto(b'overlap-udp-request',(" + fmt.Sprintf("%q", node.destination) + ",39111)); assert u.recv(128)==b'overlap-udp-reply'; s=socket.socket(socket.AF_INET,socket.SOCK_STREAM); s.bind(('10.99.99.2',42001)); s.settimeout(4); s.connect((" + fmt.Sprintf("%q", node.destination) + ",39112)); s.sendall(b'overlap-tcp-request'); assert s.recv(128)==b'overlap-tcp-reply'; s.close()"
		run("", "ip", "netns", "exec", node.namespace, "python3", "-c", python)
		if err := <-udpResult; err != nil {
			t.Fatalf("node %d UDP fixture failed: %v", node.nodeID, err)
		}
		if err := <-tcpResult; err != nil {
			t.Fatalf("node %d TCP fixture failed: %v", node.nodeID, err)
		}
		handshakes := strings.Fields(run("", "wg", "show", node.iface, "latest-handshakes"))
		if len(handshakes) != 2 || handshakes[1] == "0" {
			t.Fatalf("node %d WireGuard handshake missing: %v", node.nodeID, handshakes)
		}
	}

	for _, node := range nodes {
		deadline := time.Now().Add(3 * time.Second)
		var flows []ni.Flow
		for time.Now().Before(deadline) {
			flows = node.server.networkIntelligenceTracker.Snapshot(time.Now())
			if len(flows) >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		protocols := map[string]bool{}
		for _, flow := range flows {
			if flow.Binding.NodeID != node.nodeID || flow.Binding.UserID != node.userID || flow.Binding.Interface != node.iface || flow.Binding.Address != netip.MustParseAddr("10.99.99.2") {
				t.Fatalf("overlapping address was attributed to the wrong node/account: %+v", flow)
			}
			if flow.UploadBytes == 0 || flow.DownloadBytes == 0 {
				t.Fatalf("bidirectional traffic counters were not captured: %+v", flow)
			}
			if flow.Tuple.Source.Addr() != netip.MustParseAddr("10.99.99.2") || flow.Tuple.Destination.Addr().String() != node.destination {
				t.Fatalf("unexpected overlapping flow tuple: %+v", flow.Tuple)
			}
			protocols[flow.Tuple.Protocol] = true
		}
		if !protocols["tcp"] || !protocols["udp"] {
			t.Fatalf("node %d did not retain both isolated live protocols: %v (%d flows)", node.nodeID, protocols, len(flows))
		}
	}
}
