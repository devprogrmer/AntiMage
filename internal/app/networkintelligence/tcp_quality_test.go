package networkintelligence

import (
	"testing"
	"time"
)

func tcpPacket(seq, ack, length uint32, flags uint8) PacketMetadata {
	return PacketMetadata{Tuple: Tuple{Protocol: "tcp"}, TCPSequence: seq, TCPAcknowledgment: ack, TransportPayloadLength: length, TCPFlags: flags}
}

func TestTCPQualityRTTWrapAndKarn(t *testing.T) {
	q, _ := NewTCPQuality(8)
	now := time.Now()
	q.Observe(tcpPacket(0xfffffff0, 0, 32, 0), 0, now)
	r := q.Observe(tcpPacket(100, 16, 0, 0x10), 1, now.Add(20*time.Millisecond))
	if len(r.ACKRTT) != 1 || r.ACKRTT[0] != 20*time.Millisecond {
		t.Fatalf("wrap RTT %+v", r)
	}
	q.Observe(tcpPacket(16, 0, 20, 0), 0, now.Add(30*time.Millisecond))
	q.Observe(tcpPacket(16, 0, 20, 0), 0, now.Add(40*time.Millisecond))
	r = q.Observe(tcpPacket(100, 36, 0, 0x10), 1, now.Add(50*time.Millisecond))
	if len(r.ACKRTT) != 0 || r.ObservedRetransmissions != 1 {
		t.Fatalf("Karn %+v", r)
	}
}

func TestTCPQualityDuplicateACKCapacityAndCaptureGap(t *testing.T) {
	q, _ := NewTCPQuality(1)
	now := time.Now()
	q.Observe(tcpPacket(10, 0, 10, 0), 0, now)
	q.Observe(tcpPacket(20, 0, 10, 0), 0, now.Add(time.Millisecond))
	q.Observe(tcpPacket(50, 10, 0, 0x10), 1, now.Add(2*time.Millisecond))
	r := q.Observe(tcpPacket(50, 10, 0, 0x10), 1, now.Add(3*time.Millisecond))
	if r.ObservedDuplicateACKs != 1 || r.DiscardedSegments != 1 {
		t.Fatalf("bounded %+v", r)
	}
	r = q.Observe(tcpPacket(50, 30, 0, 0x10), 1, now.Add(4*time.Millisecond))
	if len(r.ACKRTT) != 0 {
		t.Fatal("sampled ACK beyond observed data")
	}
	if len(q.directions[0].segments) != 0 {
		t.Fatal("acknowledged segments retained")
	}
}

func TestTCPQualityStaleAndSYN(t *testing.T) {
	q, _ := NewTCPQuality(2)
	now := time.Now()
	q.Observe(tcpPacket(100, 0, 0, 2), 0, now)
	q.Observe(tcpPacket(100, 0, 0, 2), 0, now.Add(-time.Second))
	r := q.Observe(tcpPacket(200, 101, 0, 0x12), 1, now.Add(time.Millisecond))
	if len(r.ACKRTT) != 1 || r.ObservedRetransmissions != 0 {
		t.Fatalf("SYN %+v", r)
	}
	if _, err := NewTCPQuality(0); err == nil {
		t.Fatal("accepted unbounded capacity")
	}
}
