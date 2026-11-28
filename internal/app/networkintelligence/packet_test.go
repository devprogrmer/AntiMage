package networkintelligence

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func packetFixture(ipv6, tcp bool) []byte {
	header, transport := 20, 8
	if ipv6 {
		header = 40
	}
	if tcp {
		transport = 20
	}
	p := make([]byte, header+transport+3)
	protocol := byte(17)
	if tcp {
		protocol = 6
	}
	if ipv6 {
		p[0], p[6] = 0x60, protocol
		binary.BigEndian.PutUint16(p[4:6], uint16(transport+3))
		src, dst := netip.MustParseAddr("2001:db8::1").As16(), netip.MustParseAddr("2001:db8::2").As16()
		copy(p[8:24], src[:])
		copy(p[24:40], dst[:])
	} else {
		p[0], p[9] = 0x45, protocol
		binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
		copy(p[12:16], []byte{10, 1, 2, 3})
		copy(p[16:20], []byte{192, 0, 2, 1})
	}
	tr := p[header:]
	binary.BigEndian.PutUint16(tr[:2], 12345)
	binary.BigEndian.PutUint16(tr[2:4], 27051)
	if tcp {
		tr[12], tr[13] = 0x50, 0x18
		binary.BigEndian.PutUint32(tr[4:8], 100)
		binary.BigEndian.PutUint32(tr[8:12], 200)
	} else {
		binary.BigEndian.PutUint16(tr[4:6], uint16(len(tr)))
	}
	copy(tr[transport:], "abc")
	return p
}

func TestDecodePacketCopiesMetadataOnly(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			packet := packetFixture(ipv6, tcp)
			metadata, err := DecodePacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			if metadata.WireLength != uint32(len(packet)) || metadata.TransportPayloadLength != 3 || metadata.Tuple.Source.Port() != 12345 || metadata.Tuple.Destination.Port() != 27051 {
				t.Fatalf("metadata=%+v", metadata)
			}
			if tcp && (metadata.Tuple.Protocol != "tcp" || metadata.TCPSequence != 100 || metadata.TCPAcknowledgment != 200 || metadata.TCPFlags != 0x18) {
				t.Fatalf("TCP metadata=%+v", metadata)
			}
			if !tcp && metadata.Tuple.Protocol != "udp" {
				t.Fatal(metadata)
			}
			before := metadata
			for i := range packet {
				packet[i] = 0
			}
			if metadata != before {
				t.Fatal("metadata retains mutable input")
			}
		}
	}
}

func TestDecodePacketRejectsIncompleteAndAmbiguousPackets(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			packet := packetFixture(ipv6, tcp)
			for n := 0; n < len(packet); n++ {
				if _, err := DecodePacket(packet[:n]); err == nil {
					t.Fatalf("accepted truncation at %d", n)
				}
			}
		}
	}
	mutations := []func([]byte){
		func(p []byte) { p[0] = 0x44 },
		func(p []byte) { p[6] = 0x20 },
		func(p []byte) { p[7] = 1 },
		func(p []byte) { p[6] = 0x80 },
		func(p []byte) { p[24] = 0; p[25] = 7 },
		func(p []byte) { p[24] = 0; p[25] = 12 },
		func(p []byte) { p[9] = 1 },
	}
	for i, mutate := range mutations {
		packet := packetFixture(false, false)
		mutate(packet)
		if _, err := DecodePacket(packet); err == nil {
			t.Fatalf("accepted mutation %d", i)
		}
	}
	packet := packetFixture(true, false)
	packet[6] = 44
	if _, err := DecodePacket(packet); err == nil {
		t.Fatal("accepted IPv6 fragment header")
	}
	packet = packetFixture(false, true)
	packet[32] = 0xf0
	if _, err := DecodePacket(packet); err == nil {
		t.Fatal("accepted truncated TCP options")
	}
}
