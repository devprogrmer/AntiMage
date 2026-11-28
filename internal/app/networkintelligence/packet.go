package networkintelligence

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// PacketMetadata contains copied network and transport headers only. No packet
// buffer or application payload is retained.
type PacketMetadata struct {
	Tuple                  Tuple
	WireLength             uint32
	TCPSequence            uint32
	TCPAcknowledgment      uint32
	TCPFlags               uint8
	TransportPayloadLength uint32
}

// DecodePacket accepts a complete IP datagram without a link-layer header.
// Fragmented packets, IPv6 extension headers and jumbograms are rejected because
// transport attribution cannot be established from their fixed headers alone.
// Checksums are not validated here because capture offload can defer them.
func DecodePacket(packet []byte) (PacketMetadata, error) {
	return decodePacketHeaders(packet, uint32(len(packet)))
}

// decodePacketHeaders accepts a bounded header snapshot only when the capture
// source supplies the original datagram length independently of the snapshot.
func decodePacketHeaders(packet []byte, wireLength uint32) (PacketMetadata, error) {
	var metadata PacketMetadata
	if len(packet) == 0 {
		return metadata, errors.New("empty IP packet")
	}
	var source, destination netip.Addr
	var protocol byte
	var offset, total int
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return metadata, errors.New("truncated IPv4 header")
		}
		offset = int(packet[0]&15) * 4
		total = int(binary.BigEndian.Uint16(packet[2:4]))
		if offset < 20 || total < offset || offset > len(packet) || uint32(total) > wireLength {
			return metadata, errors.New("invalid IPv4 length")
		}
		fragment := binary.BigEndian.Uint16(packet[6:8])
		if fragment&0xbfff != 0 {
			return metadata, errors.New("fragmented or reserved IPv4 packet")
		}
		source = netip.AddrFrom4([4]byte(packet[12:16]))
		destination = netip.AddrFrom4([4]byte(packet[16:20]))
		protocol = packet[9]
	case 6:
		if len(packet) < 40 {
			return metadata, errors.New("truncated IPv6 header")
		}
		payload := int(binary.BigEndian.Uint16(packet[4:6]))
		if payload == 0 || uint32(40+payload) > wireLength {
			return metadata, errors.New("invalid IPv6 length")
		}
		offset, total = 40, 40+payload
		source = netip.AddrFrom16([16]byte(packet[8:24]))
		destination = netip.AddrFrom16([16]byte(packet[24:40]))
		protocol = packet[6]
	default:
		return metadata, errors.New("unsupported IP version")
	}
	transport := packet[offset:min(total, len(packet))]
	transportLength := total - offset
	var name string
	switch protocol {
	case 6:
		if len(transport) < 20 {
			return metadata, errors.New("truncated TCP header")
		}
		headerLength := int(transport[12]>>4) * 4
		if headerLength < 20 || headerLength > len(transport) {
			return metadata, errors.New("invalid TCP header length")
		}
		name = "tcp"
		metadata.TCPSequence = binary.BigEndian.Uint32(transport[4:8])
		metadata.TCPAcknowledgment = binary.BigEndian.Uint32(transport[8:12])
		metadata.TCPFlags = transport[13]
		metadata.TransportPayloadLength = uint32(transportLength - headerLength)
	case 17:
		if len(transport) < 8 {
			return metadata, errors.New("truncated UDP header")
		}
		udpLength := int(binary.BigEndian.Uint16(transport[4:6]))
		if udpLength < 8 || udpLength != transportLength {
			return metadata, errors.New("invalid UDP length")
		}
		name = "udp"
		metadata.TransportPayloadLength = uint32(udpLength - 8)
	default:
		return metadata, errors.New("unsupported transport or IPv6 extension header")
	}
	metadata.Tuple = Tuple{Source: netip.AddrPortFrom(source, binary.BigEndian.Uint16(transport[:2])), Destination: netip.AddrPortFrom(destination, binary.BigEndian.Uint16(transport[2:4])), Protocol: name}
	metadata.WireLength = uint32(total)
	return metadata, nil
}
