//go:build linux

package networkintelligence

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestTCPQualityRealCaptureACKRTT(t *testing.T) {
	if os.Getenv("ANTIMAGE_CAPTURE_E2E") != "1" {
		t.Skip("requires isolated Linux with CAP_NET_RAW")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverPort := uint16(listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	samples := make(chan time.Duration, 1)
	go func() {
		flows := make(map[uint16]*TCPQuality)
		done <- RunCapture(ctx, CaptureConfig{Interface: "lo"}, func(p CapturedPacket) {
			// Loopback exposes outgoing and host copies. Keep one observation.
			if !p.Outgoing || p.Metadata.Tuple.Protocol != "tcp" {
				return
			}
			tuple := p.Metadata.Tuple
			direction, clientPort := 0, tuple.Source.Port()
			if tuple.Source.Port() == serverPort {
				direction, clientPort = 1, tuple.Destination.Port()
			} else if tuple.Destination.Port() != serverPort {
				return
			}
			quality := flows[clientPort]
			if quality == nil {
				if len(flows) >= 64 {
					return
				}
				quality, _ = NewTCPQuality(64)
				flows[clientPort] = quality
			}
			for _, sample := range quality.Observe(p.Metadata, direction, p.At).ACKRTT {
				select {
				case samples <- sample:
				default:
				}
			}
		})
	}()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case sample := <-samples:
			if sample < 0 {
				t.Fatalf("negative RTT %s", sample)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("capture stop: %v", err)
			}
			return
		case err := <-done:
			t.Fatalf("capture stopped before sample: %v", err)
		case <-ctx.Done():
			t.Fatal("no real TCP ACK RTT sample")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			server, err := listener.Accept()
			if err != nil {
				conn.Close()
				t.Fatal(err)
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			server.SetDeadline(time.Now().Add(time.Second))
			_, writeErr := conn.Write([]byte("fixture"))
			_, readErr := io.ReadFull(server, make([]byte, 7))
			conn.Close()
			server.Close()
			if writeErr != nil || readErr != nil {
				t.Fatalf("real transfer write=%v read=%v", writeErr, readErr)
			}
		}
	}
}
