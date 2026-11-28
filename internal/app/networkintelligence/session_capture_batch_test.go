package networkintelligence

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func TestCaptureBlockTCPQualityMatchesPerPacketPublication(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	binding := Binding{NodeID: 1, UserID: 42, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	newCapture := func() (*SessionCapture, *Tracker) {
		tracker, _ := New(8, time.Minute)
		if err := tracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
		capture, err := NewSessionCapture(tracker, []Binding{binding}, 8)
		if err != nil {
			t.Fatal(err)
		}
		return capture, tracker
	}
	immediate, immediateTracker := newCapture()
	batched, batchedTracker := newCapture()
	tuple := Tuple{netip.MustParseAddrPort("10.0.0.2:1234"), netip.MustParseAddrPort("192.0.2.1:5678"), "tcp"}
	for _, sample := range []struct {
		ms               int
		outgoing         bool
		seq, ack, length uint32
	}{
		{0, false, 100, 0, 10}, {20, true, 0, 110, 0},
		{30, false, 110, 0, 10}, {32, false, 110, 0, 10}, {40, true, 0, 120, 0},
		{45, false, 120, 0, 10}, {70, true, 0, 130, 0},
	} {
		packet := CapturedPacket{Metadata: PacketMetadata{Tuple: tuple, WireLength: 40 + sample.length, TCPSequence: sample.seq, TCPAcknowledgment: sample.ack, TransportPayloadLength: sample.length, TCPFlags: 0x10}, At: now.Add(time.Duration(sample.ms) * time.Millisecond), Outgoing: sample.outgoing}
		if sample.outgoing {
			packet.Metadata.Tuple.Source, packet.Metadata.Tuple.Destination = tuple.Destination, tuple.Source
		}
		if !immediate.Consume(packet) || !batched.consume(packet, true) {
			t.Fatal("sample rejected")
		}
	}
	batched.flush()
	want := immediateTracker.Snapshot(time.Now().UTC())[0]
	got := batchedTracker.Snapshot(time.Now().UTC())[0]
	if !reflect.DeepEqual(got.TCPQuality, want.TCPQuality) || got.UploadPackets != want.UploadPackets || got.DownloadPackets != want.DownloadPackets {
		t.Fatalf("batched=%+v immediate=%+v", got, want)
	}
	if got.TCPQuality.UploadACK.Samples != 2 || got.TCPQuality.Retransmissions != 1 {
		t.Fatalf("invalid timing/retransmission fixture: %+v", got.TCPQuality)
	}
}

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
