package networkintelligence

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func udpCaptured(t *testing.T, body []byte, outgoing bool, at time.Time) CapturedPacket {
	t.Helper()
	b := make([]byte, 28+len(body))
	b[0], b[9] = 0x45, 17
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)))
	copy(b[12:16], []byte{10, 0, 0, 2})
	copy(b[16:20], []byte{192, 0, 2, 7})
	binary.BigEndian.PutUint16(b[20:], 45000)
	binary.BigEndian.PutUint16(b[22:], 27051)
	if outgoing {
		copy(b[12:16], []byte{192, 0, 2, 7})
		copy(b[16:20], []byte{10, 0, 0, 2})
		binary.BigEndian.PutUint16(b[20:], 27051)
		binary.BigEndian.PutUint16(b[22:], 45000)
	}
	binary.BigEndian.PutUint16(b[24:], uint16(8+len(body)))
	copy(b[28:], body)
	// Exercise the same 128-byte snapshot contract as the Linux collector.
	p, err := decodePacketHeaders(b[:min(128, len(b))], uint32(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return CapturedPacket{Metadata: p, Outgoing: outgoing, At: at}
}

func udpSession(t *testing.T, tracker *Tracker, node int64, now time.Time) (*SessionCapture, Binding) {
	t.Helper()
	b := Binding{NodeID: node, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), UserID: node + 40, Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return c, b
}

func TestSessionCaptureUDPProtocolsGapAndPrivacy(t *testing.T) {
	for _, fixture := range []func(bool, byte) []byte{stunFixture, gnsFixture, discordFixture} {
		now := time.Now().UTC().Add(-time.Second)
		tracker, _ := New(16, time.Minute)
		c, _ := udpSession(t, tracker, 1, now)
		if !c.consume(udpCaptured(t, fixture(false, 7), false, now), true) || !c.consume(udpCaptured(t, fixture(true, 7), true, now.Add(20*time.Millisecond)), true) {
			t.Fatal("pair rejected")
		}
		c.flush()
		c.flush() // A repeated empty batch must not duplicate counters or samples.
		flow := tracker.Snapshot(now.Add(20 * time.Millisecond))[0]
		q := flow.UDPQuality
		if q == nil || q.UploadExchange.Mean == nil || *q.UploadExchange.Mean != 20 || q.DownloadExchange.Mean != nil || q.PacketLoss != nil || flow.UploadPackets != 1 || flow.DownloadPackets != 1 {
			t.Fatalf("flow=%+v quality=%+v", flow, q)
		}
		raw, _ := json.Marshal(flow)
		for _, private := range []string{"token", "ssrc", "192.0.2.1", "private-"} {
			if strings.Contains(strings.ToLower(string(raw)), private) {
				t.Fatalf("private metadata: %s", raw)
			}
		}
		c.Consume(udpCaptured(t, fixture(false, 8), false, now.Add(30*time.Millisecond)))
		c.observeStatistics(CaptureStatistics{KernelStatisticsAvailable: true, KernelDrops: 1})
		c.Consume(udpCaptured(t, fixture(true, 8), true, now.Add(50*time.Millisecond)))
		if tracker.Snapshot(now.Add(50 * time.Millisecond))[0].UDPQuality != nil {
			t.Fatal("quality survived capture gap")
		}
		c.Stop()
		if len(c.udp) != 0 {
			t.Fatal("Stop retained correlation state")
		}
	}
}

func TestSessionCaptureUDPNodeIsolationAndReassignment(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(32, time.Minute)
	a, old := udpSession(t, tracker, 1, now)
	b, _ := udpSession(t, tracker, 2, now)
	a.Consume(udpCaptured(t, stunFixture(false, 7), false, now))
	b.Consume(udpCaptured(t, []byte("ordinary-unknown-traffic"), false, now))
	b.Consume(udpCaptured(t, stunFixture(true, 7), true, now.Add(20*time.Millisecond)))
	for _, f := range tracker.Snapshot(now.Add(20 * time.Millisecond)) {
		if f.UDPQuality != nil {
			t.Fatal("cross-node correlation")
		}
	}
	a.Stop()
	old.Generation++
	old.UserID = 99
	old.Session = "replacement"
	old.Since = now.Add(30 * time.Millisecond)
	if err := tracker.Bind(old); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{old}, 8)
	if err != nil {
		t.Fatal(err)
	}
	c.Consume(udpCaptured(t, []byte("ordinary-unknown-traffic"), false, now.Add(40*time.Millisecond)))
	c.Consume(udpCaptured(t, stunFixture(true, 7), true, now.Add(50*time.Millisecond)))
	for _, f := range tracker.Snapshot(now.Add(50 * time.Millisecond)) {
		if f.UDPQuality != nil {
			t.Fatal("reused IP inherited timing")
		}
	}
}

// Real socket I/O validates request/reply bytes and local observation timing.
// It does not replace privileged WireGuard/raw-capture staging validation.
func TestUDPRealLoopbackExchange(t *testing.T) {
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
	server.SetDeadline(time.Now().Add(3 * time.Second))
	client.SetDeadline(time.Now().Add(3 * time.Second))
	tracker, _ := New(16, time.Minute)
	now := time.Now().UTC()
	c, _ := udpSession(t, tracker, 1, now)
	defer c.Stop()
	if _, err = client.Write(stunFixture(false, 27)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, peer, err := server.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	if !c.Consume(udpCaptured(t, buf[:n], false, start)) {
		t.Fatal("request rejected")
	}
	time.Sleep(5 * time.Millisecond)
	if _, err = server.WriteToUDP(stunFixture(true, 27), peer); err != nil {
		t.Fatal(err)
	}
	n, err = client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC()
	if !c.Consume(udpCaptured(t, buf[:n], true, end)) {
		t.Fatal("response rejected")
	}
	q := tracker.Snapshot(end)[0].UDPQuality
	if q == nil || q.UploadExchange.Samples != 1 || q.UploadExchange.Mean == nil || *q.UploadExchange.Mean < float64(5) || q.PacketLoss != nil {
		t.Fatalf("socket quality=%+v", q)
	}
}

func FuzzUDPProtocolBoundedParser(f *testing.F) {
	f.Add(stunFixture(false, 7), 20)
	f.Add(gnsFixture(false, 7), 512)
	f.Add(discordFixture(true, 7), 74)
	f.Fuzz(func(t *testing.T, b []byte, wire int) {
		if len(b) > 128 {
			b = b[:128]
		}
		_ = parseUDPProtocol(b, wire)
	})
}

func BenchmarkUDPUnknownHeader(b *testing.B) {
	data := make([]byte, 100)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = parseUDPProtocol(data, 100)
	}
}

func TestSessionCaptureUDPReorderingAndBoundedCandidates(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(256, time.Minute)
	c, b := udpSession(t, tracker, 1, now)
	c.Consume(udpCaptured(t, stunFixture(false, 7), false, now))
	c.Consume(udpCaptured(t, []byte("ordinary-packet"), false, now.Add(30*time.Millisecond)))
	c.Consume(udpCaptured(t, stunFixture(true, 7), true, now.Add(20*time.Millisecond)))
	if f := tracker.Snapshot(now.Add(30 * time.Millisecond))[0]; f.UDPQuality != nil || f.UploadPackets != 2 || f.DownloadPackets != 1 {
		t.Fatalf("reordered packet corrupted counters/quality: %+v", f)
	}
	// A separate capture budget allows at most 128 candidate correlation states.
	c.Stop()
	c, err := NewSessionCapture(tracker, []Binding{b}, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for i := 0; i < 200; i++ {
		p := udpCaptured(t, stunFixture(false, 1), false, now)
		p.Metadata.Tuple.Source = netip.AddrPortFrom(b.Address, uint16(10000+i))
		c.Consume(p)
	}
	if len(c.udp) != 128 {
		t.Fatalf("unbounded correlation states: %d", len(c.udp))
	}
}
