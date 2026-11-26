package networkintelligence

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSessionCapturePublishesSeparateTCPQualityLegs(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(8, time.Minute)
	b := Binding{NodeID: 1, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{netip.MustParseAddrPort("10.0.0.2:45000"), netip.MustParseAddrPort("192.0.2.7:3724"), "tcp"}
	upload := CapturedPacket{Metadata: PacketMetadata{Tuple: tuple, WireLength: 50, TCPSequence: 100, TransportPayloadLength: 10}, At: now}
	ack := CapturedPacket{Metadata: PacketMetadata{Tuple: Tuple{tuple.Destination, tuple.Source, "tcp"}, WireLength: 40, TCPFlags: 0x10, TCPAcknowledgment: 110}, Outgoing: true, At: now.Add(20 * time.Millisecond)}
	if !c.Consume(upload) || !c.Consume(ack) {
		t.Fatal("capture rejected")
	}
	flows := tracker.Snapshot(ack.At)
	q := flows[0].TCPQuality
	if q == nil || q.Method != "tcp_ack_observation" || q.UploadACK.Samples != 1 || q.UploadACK.Mean == nil || *q.UploadACK.Mean != 20 || q.DownloadACK.Mean != nil || q.PacketLoss != nil {
		t.Fatalf("quality=%+v", q)
	}
	raw, err := json.Marshal(q)
	if err != nil || !strings.Contains(string(raw), `"packet_loss":null`) || !strings.Contains(string(raw), `"mean_ms":20`) {
		t.Fatalf("quality JSON=%s err=%v", raw, err)
	}
	// Rejected future telemetry must not poison the timing estimator.
	gap := upload
	gap.At = now.Add(2 * time.Minute)
	if c.Consume(gap) {
		t.Fatal("future packet accepted")
	}
	upload.Metadata.TCPSequence = 110
	upload.At = now.Add(30 * time.Millisecond)
	ack.Metadata.TCPAcknowledgment = 120
	ack.At = now.Add(40 * time.Millisecond)
	if !c.Consume(upload) || !c.Consume(ack) {
		t.Fatal("valid packet after future telemetry rejected")
	}
	q = tracker.Snapshot(ack.At)[0].TCPQuality
	if q.UploadACK.Samples != 2 || *q.UploadACK.Mean != 15 {
		t.Fatalf("future telemetry poisoned timing: %+v", q)
	}
}

func TestSessionCaptureStartReportsStartupFailure(t *testing.T) {
	tracker, _ := New(4, time.Minute)
	b := Binding{NodeID: 1, Interface: "ni-no-such-if", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: time.Now()}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Start(ctx, CaptureConfig{Interface: b.Interface}); err == nil {
		t.Fatal("Start reported success before opening capture socket")
	}
	if err := c.Stop(); err == nil {
		t.Fatal("Stop lost startup error")
	}
}

func TestSessionCaptureGapInvalidatesOutstandingTCPAndSummaries(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(8, time.Minute)
	b := Binding{NodeID: 1, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{netip.MustParseAddrPort("10.0.0.2:45000"), netip.MustParseAddrPort("192.0.2.7:3724"), "tcp"}
	upload := CapturedPacket{Metadata: PacketMetadata{Tuple: tuple, WireLength: 50, TCPSequence: 100, TransportPayloadLength: 10}, At: now}
	ack := CapturedPacket{Metadata: PacketMetadata{Tuple: Tuple{tuple.Destination, tuple.Source, "tcp"}, WireLength: 40, TCPFlags: 0x10, TCPAcknowledgment: 110}, Outgoing: true, At: now.Add(10 * time.Millisecond)}
	if !c.Consume(upload) || !c.Consume(ack) {
		t.Fatal("first sample rejected")
	}
	upload.Metadata.TCPSequence = 110
	upload.At = now.Add(20 * time.Millisecond)
	if !c.Consume(upload) {
		t.Fatal("outstanding segment rejected")
	}
	c.observeStatistics(CaptureStatistics{KernelStatisticsAvailable: true, KernelDrops: 1})
	if tracker.Snapshot(upload.At)[0].TCPQuality != nil {
		t.Fatal("gap left published quality valid")
	}
	ack.Metadata.TCPAcknowledgment = 120
	ack.At = now.Add(30 * time.Millisecond)
	if !c.Consume(ack) {
		t.Fatal("post-gap ACK rejected")
	}
	q := tracker.Snapshot(ack.At)[0].TCPQuality
	if q.UploadACK.Samples != 0 {
		t.Fatal("RTT measured across capture gap")
	}
	upload.Metadata.TCPSequence = 120
	upload.At = now.Add(40 * time.Millisecond)
	ack.Metadata.TCPAcknowledgment = 130
	ack.At = now.Add(50 * time.Millisecond)
	if !c.Consume(upload) || !c.Consume(ack) {
		t.Fatal("fresh timing rejected")
	}
	q = tracker.Snapshot(ack.At)[0].TCPQuality
	if q.UploadACK.Samples != 1 || *q.UploadACK.Mean != 10 {
		t.Fatalf("fresh quality=%+v", q)
	}
	c.observeStatistics(CaptureStatistics{KernelStatisticsAvailable: true, KernelDrops: 1})
	if tracker.Snapshot(ack.At)[0].TCPQuality == nil {
		t.Fatal("unchanged cumulative counters reset timing")
	}
	callbackDone := make(chan struct{})
	go func() {
		c.captureStatisticsCallback(func(s CaptureStatistics) {
			// Reentering the consumer from the caller callback must not hold c.mu.
			c.Consume(CapturedPacket{})
			tracker.Snapshot(ack.At)
			if s.KernelDrops != 2 {
				t.Error("statistics callback value lost")
			}
		})(CaptureStatistics{KernelStatisticsAvailable: true, KernelDrops: 2})
		close(callbackDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("statistics callback deadlocked")
	}
}

func TestSessionCaptureDirectionOwnershipAndStop(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	tracker, _ := New(8, time.Minute)
	b := Binding{NodeID: 1, Interface: "wg-test", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: now.Add(-time.Second)}
	if _, err := NewSessionCapture(tracker, []Binding{b}, 8); err == nil {
		t.Fatal("accepted unpublished ownership")
	}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSessionCapture(tracker, []Binding{b, b}, 8); err == nil {
		t.Fatal("accepted ambiguous ownership")
	}
	c, err := NewSessionCapture(tracker, []Binding{b}, 8)
	if err != nil {
		t.Fatal(err)
	}
	upload := CapturedPacket{Metadata: PacketMetadata{Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:45000"), netip.MustParseAddrPort("192.0.2.7:27051"), "udp"}, WireLength: 100}, At: now}
	download := upload
	download.Outgoing = true
	download.Metadata.Tuple.Source, download.Metadata.Tuple.Destination = upload.Metadata.Tuple.Destination, upload.Metadata.Tuple.Source
	if c.Consume(download) {
		t.Fatal("accepted download without current upload")
	}
	if !c.Consume(upload) || !c.Consume(download) {
		t.Fatal("valid direction rejected")
	}
	flows := tracker.Snapshot(now)
	if len(flows) != 1 || flows[0].UploadBytes != 100 || flows[0].DownloadBytes != 100 {
		t.Fatalf("flows=%+v", flows)
	}
	replacement := b
	replacement.UserID++
	replacement.Generation++
	if err := tracker.Bind(replacement); err != nil {
		t.Fatal(err)
	}
	if c.Consume(upload) {
		t.Fatal("old collector survived reassignment")
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if c.Consume(upload) {
		t.Fatal("consumed after Stop")
	}
}
