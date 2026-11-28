package nodeagent

import (
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

func TestNetworkIntelligenceUDPActualCaptureToSnapshot(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	agent := New(Config{NetworkIntelligenceEnabled: true, DataDir: t.TempDir()})
	defer agent.stopNetworkIntelligence()
	tracker := agent.networkIntelligenceTracker
	b := ni.Binding{NodeID: 7, UserID: 42, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), Session: "private-peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := ni.NewSessionCapture(tracker, []ni.Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for i := 0; i < 2; i++ {
		packet := make([]byte, 48)
		packet[0] = 0x45
		packet[9] = 17
		binary.BigEndian.PutUint16(packet[2:], 48)
		copy(packet[12:16], []byte{10, 0, 0, 2})
		copy(packet[16:20], []byte{192, 0, 2, 7})
		binary.BigEndian.PutUint16(packet[20:], 45000)
		binary.BigEndian.PutUint16(packet[22:], 3478)
		binary.BigEndian.PutUint16(packet[24:], 28)
		binary.BigEndian.PutUint16(packet[28:], 1)
		binary.BigEndian.PutUint32(packet[32:], 0x2112a442)
		copy(packet[36:], []byte("secret-nonce"))
		if i == 1 {
			copy(packet[12:16], []byte{192, 0, 2, 7})
			copy(packet[16:20], []byte{10, 0, 0, 2})
			binary.BigEndian.PutUint16(packet[20:], 3478)
			binary.BigEndian.PutUint16(packet[22:], 45000)
			binary.BigEndian.PutUint16(packet[28:], 0x101)
		}
		metadata, err := ni.DecodePacket(packet)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Consume(ni.CapturedPacket{Metadata: metadata, Outgoing: i == 1, At: now.Add(time.Duration(i) * 20 * time.Millisecond)}) {
			t.Fatal("capture rejected")
		}
	}
	raw := agent.networkIntelligenceSnapshot()
	var snapshot struct {
		Flows []struct {
			Quality     *ni.UDPFlowQuality      `json:"udp_quality"`
			Application ni.ApplicationDetection `json:"application"`
		} `json:"flows"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Flows) != 1 {
		t.Fatal(raw)
	}
	flow := snapshot.Flows[0]
	if flow.Quality == nil || flow.Quality.UploadExchange.Mean == nil || *flow.Quality.UploadExchange.Mean != 20 || flow.Quality.PacketLoss != nil || flow.Application.Application != "STUN connectivity checks" || flow.Application.Scope != "protocol" {
		t.Fatal(raw)
	}
	if strings.Contains(raw, "secret-nonce") || strings.Contains(raw, "private-peer") {
		t.Fatal("private correlation/session leaked")
	}
	disabled := New(Config{DataDir: t.TempDir()})
	defer disabled.stopNetworkIntelligence()
	if disabled.networkIntelligenceDetector != nil || disabled.networkIntelligenceSnapshot() != "" {
		t.Fatal("disabled module initialized detection")
	}
}
