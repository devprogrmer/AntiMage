package networkintelligence

import (
	"net/netip"
	"testing"
	"time"
)

func TestOwnershipAndCounters(t *testing.T) {
	now := time.Now()
	tracker, _ := New(4, time.Minute)
	b := Binding{1, "wg-a", netip.MustParseAddr("10.0.0.2"), 42, "session-a", 1, now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	o := Observation{Binding: b, Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:45000"), netip.MustParseAddrPort("192.0.2.7:27051"), "udp"}, Sequence: 1, At: now, UploadPackets: 2, UploadBytes: 100}
	if !tracker.Observe(o, now) || tracker.Observe(o, now) {
		t.Fatal("fresh observation or duplicate handling")
	}
	replacement := b
	replacement.Generation++
	replacement.UserID = 43
	replacement.Session = "session-b"
	replacement.Since = now
	if err := tracker.Bind(replacement); err != nil {
		t.Fatal(err)
	}
	if tracker.Observe(o, now) || len(tracker.Snapshot(now)) != 0 {
		t.Fatal("old owner revived")
	}
	if tracker.Bind(b) == nil {
		t.Fatal("accepted stale lease")
	}
	o.Binding = replacement
	o.Sequence = 2
	if !tracker.Observe(o, now) {
		t.Fatal("new owner rejected")
	}
	tracker.Unbind(replacement)
	if tracker.Observe(o, now) {
		t.Fatal("disconnected owner accepted")
	}
}

func TestNodeInterfaceScopeCapacityAndExpiry(t *testing.T) {
	now := time.Now()
	tracker, _ := New(3, time.Second)
	b := Binding{1, "wg-a", netip.MustParseAddr("10.0.0.2"), 42, "s", 1, now}
	for i := 0; i < 3; i++ {
		copy := b
		if i == 1 {
			copy.NodeID = 2
		}
		if i == 2 {
			copy.Interface = "wg-b"
		}
		if err := tracker.Bind(copy); err != nil {
			t.Fatal(err)
		}
		o := Observation{Binding: copy, Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:1000"), netip.MustParseAddrPort("192.0.2.1:3724"), "tcp"}, Sequence: 1, At: now}
		if !tracker.Observe(o, now) {
			t.Fatal("scoped observation rejected")
		}
		o.Tuple.Destination = netip.MustParseAddrPort("192.0.2.2:443")
		if i == 2 && tracker.Observe(o, now) {
			t.Fatal("flow cap exceeded")
		}
	}
	if len(tracker.Snapshot(now.Add(time.Second))) != 0 {
		t.Fatal("flows did not expire")
	}
}
