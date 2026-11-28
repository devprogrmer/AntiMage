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

func TestSameAddressAndFlowOnDifferentNodesRemainIsolated(t *testing.T) {
	now := time.Now().UTC()
	tracker, err := New(4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("10.0.0.2")
	bindingA := Binding{NodeID: 1, Interface: "wg0", Address: address, UserID: 101, Session: "node-a-peer", Generation: 1, Since: now.Add(-time.Second)}
	bindingB := Binding{NodeID: 2, Interface: "wg0", Address: address, UserID: 202, Session: "node-b-peer", Generation: 1, Since: now.Add(-time.Second)}
	for _, binding := range []Binding{bindingA, bindingB} {
		if err := tracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
	}
	tuple := Tuple{
		Source:      netip.MustParseAddrPort("10.0.0.2:45000"),
		Destination: netip.MustParseAddrPort("198.51.100.7:27051"),
		Protocol:    "udp",
	}
	for _, observation := range []Observation{
		{CollectorEpoch: tracker.Epoch(), Binding: bindingA, Tuple: tuple, Sequence: 1, At: now, UploadPackets: 3, UploadBytes: 300},
		{CollectorEpoch: tracker.Epoch(), Binding: bindingB, Tuple: tuple, Sequence: 1, At: now, UploadPackets: 7, UploadBytes: 700},
	} {
		if !tracker.Observe(observation, now) {
			t.Fatalf("overlapping node flow rejected: node=%d", observation.Binding.NodeID)
		}
	}

	flows := tracker.Snapshot(now)
	if len(flows) != 2 {
		t.Fatalf("identical address and tuple on two nodes produced %d flows, want 2", len(flows))
	}
	byNode := make(map[int64]Flow, len(flows))
	for _, flow := range flows {
		byNode[flow.Binding.NodeID] = flow
	}
	if byNode[1].Binding.UserID != 101 || byNode[1].UploadBytes != 300 || byNode[2].Binding.UserID != 202 || byNode[2].UploadBytes != 700 {
		t.Fatalf("overlapping node telemetry was mixed: node1=%+v node2=%+v", byNode[1], byNode[2])
	}

	replacement := bindingA
	replacement.UserID = 303
	replacement.Session = "node-a-reassigned-peer"
	replacement.Generation++
	replacement.Since = now.Add(time.Millisecond)
	if err := tracker.Bind(replacement); err != nil {
		t.Fatal(err)
	}
	if tracker.Observe(Observation{CollectorEpoch: tracker.Epoch(), Binding: bindingA, Tuple: tuple, Sequence: 2, At: now.Add(2 * time.Millisecond)}, now.Add(2*time.Millisecond)) {
		t.Fatal("late observation from reassigned node A was accepted")
	}
	flows = tracker.Snapshot(now.Add(2 * time.Millisecond))
	if len(flows) != 1 || flows[0].Binding.NodeID != 2 || flows[0].Binding.UserID != 202 || flows[0].UploadBytes != 700 {
		t.Fatalf("node A reassignment disturbed overlapping node B: %+v", flows)
	}
}

func TestBindingsForUserAreScopedToNodeAndIncludeAllInterfaces(t *testing.T) {
	tracker, err := New(8, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	bindings := []Binding{
		{NodeID: 7, Interface: "wg-a", Address: netip.MustParseAddr("10.0.0.1"), UserID: 42, Session: "session-a", Generation: 1, Since: now},
		{NodeID: 7, Interface: "wg-b", Address: netip.MustParseAddr("10.0.1.1"), UserID: 42, Session: "session-b", Generation: 1, Since: now},
		{NodeID: 8, Interface: "wg-a", Address: netip.MustParseAddr("10.0.0.1"), UserID: 42, Session: "session-c", Generation: 1, Since: now},
	}
	for _, binding := range bindings {
		if err := tracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
	}
	got := tracker.BindingsForUser(7, 42)
	if len(got) != 2 {
		t.Fatalf("node 7 bindings = %d, want 2: %+v", len(got), got)
	}
	if got[0].NodeID != 7 || got[1].NodeID != 7 || got[0].UserID != 42 || got[1].UserID != 42 {
		t.Fatalf("lookup crossed authoritative node/account scope: %+v", got)
	}
	if got[0].Interface == got[1].Interface || got[0].Address == got[1].Address {
		t.Fatalf("lookup omitted one of the user's interfaces/addresses: %+v", got)
	}
}

func TestBindingsForAddressDetectsSameNodeAmbiguityButNotOtherNodes(t *testing.T) {
	tracker, err := New(8, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	address := netip.MustParseAddr("10.0.0.9")
	for _, binding := range []Binding{
		{NodeID: 7, Interface: "wg-a", Address: address, UserID: 42, Session: "a", Generation: 1, Since: now},
		{NodeID: 7, Interface: "wg-b", Address: address, UserID: 43, Session: "b", Generation: 1, Since: now},
		{NodeID: 8, Interface: "wg-a", Address: address, UserID: 44, Session: "c", Generation: 1, Since: now},
	} {
		if err := tracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
	}
	if got := tracker.BindingsForAddress(7, address); len(got) != 2 {
		t.Fatalf("node-local ambiguity candidates=%+v, want both owners", got)
	}
	if got := tracker.BindingsForAddress(8, address); len(got) != 1 || got[0].UserID != 44 {
		t.Fatalf("other node overlap was mixed: %+v", got)
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
