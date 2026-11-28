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

func TestCaptureUDPProtocolRealExchange(t *testing.T) {
	if os.Getenv("ANTIMAGE_CAPTURE_E2E") != "1" {
		t.Skip("requires disposable Linux and CAP_NET_RAW")
	}
	if _, err := os.Stat("/run/antimage-disposable-network-lab"); err != nil {
		t.Fatal("disposable guest marker required")
	}
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server.SetDeadline(time.Now().Add(5 * time.Second))
	client.SetDeadline(time.Now().Add(5 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	result := make(chan *UDPFlowQuality, 1)
	q := newUDPQuality(udpSTUN)
	serverPort := uint16(server.LocalAddr().(*net.UDPAddr).Port)
	clientPort := uint16(client.LocalAddr().(*net.UDPAddr).Port)
	go func() {
		done <- runCapture(ctx, CaptureConfig{Interface: "lo"}, func(p CapturedPacket) {
			// Loopback produces both packet-socket directions; use one observation.
			if p.Outgoing || p.Metadata.Tuple.Protocol != "udp" {
				return
			}
			tuple := p.Metadata.Tuple
			request := tuple.Source.Port() == clientPort && tuple.Destination.Port() == serverPort
			reply := tuple.Source.Port() == serverPort && tuple.Destination.Port() == clientPort
			if !request && !reply {
				return
			}
			q.observe(p.Metadata.udp, reply, p.At)
			if quality := q.snapshot(p.At); quality != nil {
				select {
				case result <- quality:
				default:
				}
			}
		}, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Always join the capture before resources leave this scope.
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	}()
	if _, err = client.Write(stunFixture(false, 93)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	_, peer, err := server.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err = server.WriteToUDP(stunFixture(true, 93), peer); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Read(buf); err != nil {
		t.Fatal(err)
	}
	select {
	case quality := <-result:
		if quality.UploadExchange.Samples != 1 || quality.UploadExchange.Mean == nil || *quality.UploadExchange.Mean < 5 || quality.PacketLoss != nil {
			t.Fatalf("kernel quality=%+v", quality)
		}
	case <-ctx.Done():
		t.Fatal("kernel did not publish valid UDP transaction timing")
	}
}
