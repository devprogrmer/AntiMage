package networkintelligence

import (
	"net/netip"
	"testing"
	"time"
)

func TestCaptureBlockPublicationPreservesCountersAndRejectsRevokedOwner(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(8, time.Minute)
	binding := Binding{NodeID: 1, UserID: 42, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(binding); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{binding}, 8)
	if err != nil {
		t.Fatal(err)
	}
	packet := CapturedPacket{Metadata: PacketMetadata{Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:1234"), netip.MustParseAddrPort("192.0.2.1:5678"), "udp"}, WireLength: 100}, At: now}
	for i := 0; i < 100; i++ {
		packet.At = now.Add(time.Duration(i) * time.Millisecond)
		if !c.consume(packet, true) {
			t.Fatal("valid packet rejected")
		}
	}
	if len(tracker.Snapshot(time.Now().UTC())) != 0 {
		t.Fatal("partial block published")
	}
	c.flush()
	flows := tracker.Snapshot(time.Now().UTC())
	if len(flows) != 1 || flows[0].UploadPackets != 100 || flows[0].UploadBytes != 10000 {
		t.Fatalf("counters=%+v", flows)
	}
	c.flush()
	if tracker.Snapshot(time.Now().UTC())[0].UploadPackets != 100 {
		t.Fatal("duplicate flush changed counters")
	}
	packet.At = now.Add(101 * time.Millisecond)
	if !c.consume(packet, true) {
		t.Fatal("packet rejected")
	}
	tracker.Unbind(binding)
	c.flush()
	if len(tracker.Snapshot(time.Now().UTC())) != 0 || len(c.flows) != 0 {
		t.Fatal("deferred block revived revoked owner")
	}
}

func TestCaptureReorderedKernelTimestampsKeepCountsAndInvalidateTiming(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(8, time.Minute)
	b := Binding{NodeID: 1, UserID: 42, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	packet := CapturedPacket{Metadata: PacketMetadata{Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:1234"), netip.MustParseAddrPort("192.0.2.1:5678"), "tcp"}, WireLength: 40}, At: now}
	if !c.Consume(packet) {
		t.Fatal("first packet rejected")
	}
	packet.At = now.Add(-time.Millisecond)
	if !c.Consume(packet) {
		t.Fatal("reordered kernel packet lost from counters")
	}
	flows := tracker.Snapshot(time.Now().UTC())
	if len(flows) != 1 || flows[0].UploadPackets != 2 || !flows[0].At.Equal(now) || flows[0].TCPQuality != nil {
		t.Fatalf("reordered observation=%+v", flows)
	}
	packet.At = b.Since.Add(-time.Nanosecond)
	if c.Consume(packet) {
		t.Fatal("pre-lease packet accepted")
	}
}
