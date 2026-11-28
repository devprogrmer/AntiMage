package networkintelligence

import (
	"bytes"
	"encoding/binary"
	"net/netip"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	udpSTUN uint8 = iota + 1
	udpGNS
	udpDiscord
	udpSourceQuery
)

// Correlation identifiers never leave local bounded collector state. No body,
// address discovered by STUN/Discord, identity, key or voice data is retained.
type udpProtocolMetadata struct {
	protocol, kind uint8
	token          [16]byte
	appID          uint32
}

func parseUDPProtocol(b []byte, wireLength int) udpProtocolMetadata {
	var result udpProtocolMetadata
	if len(b) < 8 || wireLength < len(b) {
		return result
	}
	if binary.LittleEndian.Uint32(b[:4]) == 0xffffffff {
		return parseSourceQuery(b, wireLength)
	}
	if len(b) >= 20 && binary.BigEndian.Uint32(b[4:8]) == 0x2112a442 {
		typeID := binary.BigEndian.Uint16(b[:2])
		if (typeID != 1 && typeID != 0x101 && typeID != 0x111) || int(binary.BigEndian.Uint16(b[2:4]))+20 != wireLength || binary.BigEndian.Uint16(b[2:4])%4 != 0 {
			return result
		}
		result.protocol, result.kind = udpSTUN, 1
		if typeID != 1 {
			result.kind = 2
		}
		copy(result.token[:], b[8:20])
		return result
	}
	if wireLength == 74 && len(b) >= 74 && binary.BigEndian.Uint16(b[2:4]) == 70 {
		typeID := binary.BigEndian.Uint16(b[:2])
		if typeID != 1 && typeID != 2 {
			return result
		}
		if typeID == 1 {
			for _, value := range b[8:74] {
				if value != 0 {
					return result
				}
			}
		} else {
			end := bytes.IndexByte(b[8:72], 0)
			if end <= 0 || binary.BigEndian.Uint16(b[72:74]) == 0 {
				return result
			}
			address, err := netip.ParseAddr(string(b[8 : 8+end]))
			if err != nil || address.IsUnspecified() || address.IsMulticast() {
				return result
			}
		}
		result.protocol, result.kind = udpDiscord, uint8(typeID)
		copy(result.token[:4], b[4:8])
		return result
	}
	if b[0] != 32 && b[0] != 33 {
		return result
	}
	kind := uint8(2)
	message := b[1:]
	if b[0] == 32 {
		if wireLength < 512 {
			return result
		}
		length := int(binary.LittleEndian.Uint16(b[1:3]))
		if length == 0 || length > 80 || length > len(b)-3 || length > wireLength-3 {
			return result
		}
		message = b[3 : 3+length]
		kind = 1
	} else if wireLength != len(b) || len(message) > 80 {
		return result
	}
	var connection uint32
	var timestamp uint64
	var seen uint8
	for len(message) > 0 {
		field, wire, n := protowire.ConsumeTag(message)
		if n < 0 || field < 1 || field > 4 || seen&(1<<field) != 0 {
			return result
		}
		seen |= 1 << field
		message = message[n:]
		switch field {
		case 1:
			if wire != protowire.Fixed32Type {
				return result
			}
			value, n := protowire.ConsumeFixed32(message)
			if n < 0 || value == 0 {
				return result
			}
			connection = value
			message = message[n:]
		case 2, 3:
			if wire != protowire.Fixed64Type {
				return result
			}
			value, n := protowire.ConsumeFixed64(message)
			if n < 0 {
				return result
			}
			if field == 3 {
				if value == 0 {
					return result
				}
				timestamp = value
			}
			message = message[n:]
		case 4:
			if wire != protowire.VarintType {
				return result
			}
			value, n := protowire.ConsumeVarint(message)
			if n < 0 || value == 0 || value > 0xffffffff {
				return result
			}
			message = message[n:]
		}
	}
	if seen&0x1a != 0x1a || (kind == 2 && seen&4 == 0) {
		return result
	}
	result.protocol, result.kind = udpGNS, kind
	binary.LittleEndian.PutUint32(result.token[:4], connection)
	binary.LittleEndian.PutUint64(result.token[4:12], timestamp)
	return result
}
