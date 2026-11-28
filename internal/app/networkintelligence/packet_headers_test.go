package networkintelligence

import (
	"encoding/binary"
	"testing"
)

func TestHeaderSnapshotPreservesOriginalLengthsAndTCPMetadata(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			packet := append(packetFixture(ipv6, tcp), make([]byte, 1000)...)
			offset := 20
			if ipv6 {
				offset = 40
				binary.BigEndian.PutUint16(packet[4:], uint16(len(packet)-40))
			} else {
				binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
			}
			if !tcp {
				binary.BigEndian.PutUint16(packet[offset+4:], uint16(len(packet)-offset))
			}
			want, err := DecodePacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodePacketHeaders(packet[:128], uint32(len(packet)))
			if err != nil || got != want {
				t.Fatalf("ipv6=%t tcp=%t got=%+v want=%+v err=%v", ipv6, tcp, got, want, err)
			}
			if _, err := DecodePacket(packet[:128]); err == nil {
				t.Fatal("public full-packet decoder accepted truncation")
			}
			if _, err := decodePacketHeaders(packet[:128], 128); err == nil {
				t.Fatal("unverified original length accepted")
			}
			header := 8
			if tcp {
				header = 20
			}
			if _, err := decodePacketHeaders(packet[:offset+header-1], uint32(len(packet))); err == nil {
				t.Fatal("partial transport header accepted")
			}
		}
	}
	// Maximum IPv4 and TCP option lengths still fit in the bounded snapshot.
	packet := make([]byte, 1120)
	packet[0] = 0x4f
	packet[9] = 6
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	packet[60+12] = 0xf0
	want, err := DecodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePacketHeaders(packet[:128], uint32(len(packet)))
	if err != nil || got != want || got.TransportPayloadLength != 1000 {
		t.Fatalf("maximum options: %+v err=%v", got, err)
	}
	if _, err := decodePacketHeaders(packet[:119], uint32(len(packet))); err == nil {
		t.Fatal("partial TCP options accepted")
	}
}
