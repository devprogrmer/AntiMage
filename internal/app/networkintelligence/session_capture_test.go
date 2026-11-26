package networkintelligence

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

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
