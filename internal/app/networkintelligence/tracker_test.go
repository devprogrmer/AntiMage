package networkintelligence

import (
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func BenchmarkTrackerObserve(b *testing.B) {
	for _, flowCount := range []int{1, 1024, 4096} {
		b.Run(strconv.Itoa(flowCount), func(b *testing.B) {
			now := time.Now()
			tracker, err := New(flowCount, time.Minute)
			if err != nil {
				b.Fatal(err)
			}
			binding := Binding{1, "wg-a", netip.MustParseAddr("10.0.0.2"), 42, "session", 1, now}
			if err := tracker.Bind(binding); err != nil {
				b.Fatal(err)
			}
			observation := Observation{CollectorEpoch: tracker.Epoch(), Binding: binding, Tuple: Tuple{Source: netip.AddrPortFrom(binding.Address, 1000), Protocol: "udp"}, Sequence: 1, At: now}
			destination := netip.MustParseAddr("192.0.2.1")
			for port := 1; port <= flowCount; port++ {
				observation.Tuple.Destination = netip.AddrPortFrom(destination, uint16(port))
				if !tracker.Observe(observation, now) {
					b.Fatal("could not populate tracker")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				observation.Sequence++
				if !tracker.Observe(observation, now) {
					b.Fatal("could not update flow")
				}
			}
		})
	}
}

func TestOwnershipAndCounters(t *testing.T) {
	now := time.Now()
	tracker, _ := New(4, time.Minute)
	b := Binding{1, "wg-a", netip.MustParseAddr("10.0.0.2"), 42, "session-a", 1, now.Add(-time.Second)}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	o := Observation{CollectorEpoch: tracker.Epoch(), Binding: b, Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:45000"), netip.MustParseAddrPort("192.0.2.7:27051"), "udp"}, Sequence: 1, At: now, UploadPackets: 2, UploadBytes: 100}
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
		o := Observation{CollectorEpoch: tracker.Epoch(), Binding: copy, Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:1000"), netip.MustParseAddrPort("192.0.2.1:3724"), "tcp"}, Sequence: 1, At: now}
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

func TestExpiredFlowRetainsReplayHighWater(t *testing.T) {
	now := time.Now()
	tracker, _ := New(1, time.Second)
	b := Binding{1, "wg-a", netip.MustParseAddr("10.0.0.2"), 42, "session", 1, now}
	if err := tracker.Bind(b); err != nil {
		t.Fatal(err)
	}
	o := Observation{CollectorEpoch: tracker.Epoch(), Binding: b, Tuple: Tuple{netip.MustParseAddrPort("10.0.0.2:1000"), netip.MustParseAddrPort("192.0.2.1:443"), "tcp"}, Sequence: 10, At: now, UploadBytes: 100}
	if !tracker.Observe(o, now) {
		t.Fatal("fresh flow rejected")
	}
	later := now.Add(2 * time.Second)
	if len(tracker.Snapshot(later)) != 0 {
		t.Fatal("expired flow visible")
	}
	// A delayed exporter can timestamp an old counter snapshot at send time.
	o.At = later
	if tracker.Observe(o, later) {
		t.Fatal("expired sequence replay accepted")
	}
	o.Sequence++
	o.UploadBytes = 90
	if tracker.Observe(o, later) {
		t.Fatal("expired counter regression accepted")
	}
	o.UploadBytes = 120
	if !tracker.Observe(o, later) {
		t.Fatal("fresh cumulative continuation rejected")
	}
	restarted, _ := New(1, time.Second)
	if err := restarted.Bind(b); err != nil {
		t.Fatal(err)
	}
	if restarted.Observe(o, later) {
		t.Fatal("previous collector epoch accepted after restart")
	}
	o.Tuple.Destination = netip.MustParseAddrPort("192.0.2.2:443")
	if tracker.Observe(o, later) {
		t.Fatal("high-water capacity exceeded")
	}
}
