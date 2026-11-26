//go:build linux

package networkintelligence

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestCaptureLoopbackRealTCPUDP(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(chan string, 64)
	done := make(chan error, 1)
	var statistics CaptureStatistics
	go func() {
		done <- RunCapture(ctx, CaptureConfig{Interface: "lo", Statistics: func(s CaptureStatistics) { statistics = s }}, func(p CapturedPacket) {
			port := int(p.Metadata.Tuple.Destination.Port())
			if port == udp.LocalAddr().(*net.UDPAddr).Port || port == tcp.Addr().(*net.TCPAddr).Port {
				select {
				case seen <- p.Metadata.Tuple.Protocol:
				default:
				}
			}
		})
	}()
	// Repeated real traffic avoids relying on a capture-startup sleep.
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	found := make(map[string]bool)
	for len(found) < 2 {
		select {
		case protocol := <-seen:
			found[protocol] = true
		case err := <-done:
			t.Fatalf("capture ended early: %v", err)
		case <-ctx.Done():
			t.Fatalf("missing TCP/UDP metadata: %v", found)
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
			if err != nil {
				conn.Close()
				t.Fatal(err)
			}
			conn.Close()
			accepted.Close()
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		if statistics.DecodedPackets < 2 || statistics.ReceivedPackets != statistics.DecodedPackets+statistics.RejectedPackets || !statistics.KernelStatisticsAvailable || statistics.KernelPackets < statistics.ReceivedPackets || statistics.ReceiveBufferBytes <= 0 {
			t.Fatalf("invalid capture statistics: %+v", statistics)
		}
	case <-time.After(time.Second):
		t.Fatal("capture did not stop after cancellation")
	}
}
