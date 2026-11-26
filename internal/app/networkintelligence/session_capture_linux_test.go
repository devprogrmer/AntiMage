//go:build linux

package networkintelligence

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestSessionCaptureRealTransportEndpoints(t *testing.T) {
	if os.Getenv("ANTIMAGE_CAPTURE_E2E") != "1" {
		t.Skip("requires isolated Linux with CAP_NET_RAW")
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tracker, err := New(256, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{NodeID: 7, Interface: "lo", Address: netip.MustParseAddr("127.0.0.1"), UserID: 42, Session: "isolated-fixture", Generation: 1, Since: time.Now().Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	capture, err := NewSessionCapture(tracker, []Binding{b}, 256)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := capture.Start(ctx, CaptureConfig{Interface: "lo"}); err != nil {
		t.Fatal(err)
	}
	defer capture.Stop()
	udpPort := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	tcpPort := uint16(tcp.Addr().(*net.TCPAddr).Port)
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		foundTCP, foundUDP := false, false
		for _, flow := range tracker.Snapshot(time.Now()) {
			if flow.Binding != b || flow.CollectorEpoch != tracker.Epoch() {
				t.Fatal("wrong ownership or epoch")
			}
			foundTCP = foundTCP || (flow.Tuple.Protocol == "tcp" && flow.Tuple.Destination.Port() == tcpPort)
			foundUDP = foundUDP || (flow.Tuple.Protocol == "udp" && flow.Tuple.Destination.Port() == udpPort)
		}
		if foundTCP && foundUDP {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("missing actual transport endpoints")
		case <-ticker.C:
			conn, err := net.DialTimeout("udp4", udp.LocalAddr().String(), 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			_, err = conn.Write([]byte("fixture"))
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			conn, err = net.DialTimeout("tcp4", tcp.Addr().String(), 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := tcp.Accept()
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			accepted.Close()
		}
	}
	if err := capture.Stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop=%v", err)
	}
	// Loopback produces multiple observation points, so this test asserts real
	// endpoints and ownership only. WireGuard packet-count acceptance needs a
	// controlled single-point namespace fixture.
}
