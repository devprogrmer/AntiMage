package networkintelligence

import (
	"encoding/binary"
	"testing"
)

func stunFixture(response bool, nonce byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b, 1)
	if response {
		binary.BigEndian.PutUint16(b, 0x101)
	}
	binary.BigEndian.PutUint32(b[4:], 0x2112a442)
	b[8] = nonce
	return b
}

func gnsFixture(response bool, nonce byte) []byte {
	// Valve's direct-UDP challenge framing and protobuf field types.
	message := []byte{0x0d, 1, 0, 0, 0, 0x19, nonce, 0, 0, 0, 0, 0, 0, 0, 0x20, 13}
	if response {
		return append([]byte{33}, append(message, 0x11, 1, 0, 0, 0, 0, 0, 0, 0)...)
	}
	b := make([]byte, 512)
	b[0] = 32
	binary.LittleEndian.PutUint16(b[1:], uint16(len(message)))
	copy(b[3:], message)
	return b
}

func discordFixture(response bool, nonce byte) []byte {
	b := make([]byte, 74)
	binary.BigEndian.PutUint16(b, 1)
	binary.BigEndian.PutUint16(b[2:], 70)
	b[7] = nonce
	if response {
		binary.BigEndian.PutUint16(b, 2)
		copy(b[8:], "192.0.2.1")
		binary.BigEndian.PutUint16(b[72:], 40000)
	}
	return b
}

func TestUDPProtocolHeaderPairsAndBoundedSnapshots(t *testing.T) {
	for _, fixture := range []func(bool, byte) []byte{stunFixture, gnsFixture, discordFixture} {
		request, response := fixture(false, 7), fixture(true, 7)
		a := parseUDPProtocol(request[:min(100, len(request))], len(request))
		b := parseUDPProtocol(response, len(response))
		if a.protocol == 0 || a.kind != 1 || b.kind != 2 || a.protocol != b.protocol || a.token != b.token {
			t.Fatalf("unmatched protocol headers: %+v %+v", a, b)
		}
		if got := parseUDPProtocol(request[:3], len(request)); got.protocol != 0 {
			t.Fatal("short header classified")
		}
		if got := parseUDPProtocol(response, len(response)+1); got.protocol != 0 {
			t.Fatal("invalid datagram length classified")
		}
	}
	bad := stunFixture(false, 1)
	bad[4] = 0
	if parseUDPProtocol(bad, len(bad)).protocol != 0 {
		t.Fatal("invalid STUN cookie classified")
	}
	bad = discordFixture(false, 1)
	bad[20] = 1
	if parseUDPProtocol(bad, len(bad)).protocol != 0 {
		t.Fatal("invalid Discord discovery padding classified")
	}
	bad = gnsFixture(false, 1)
	binary.LittleEndian.PutUint16(bad[1:], 500)
	if parseUDPProtocol(bad[:100], len(bad)).protocol != 0 {
		t.Fatal("incomplete challenge protobuf classified")
	}
	if parseUDPProtocol([]byte("ordinary UDP on port 27051"), 25).protocol != 0 {
		t.Fatal("ordinary UDP guessed as a game")
	}
}
