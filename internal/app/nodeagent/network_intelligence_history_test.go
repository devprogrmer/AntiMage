package nodeagent

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

func historyTestFlow(now time.Time, destination string, samples int, mean float64, retransmissions uint64) ni.Flow {
	return ni.Flow{Observation: ni.Observation{
		Binding:    ni.Binding{NodeID: 7, UserID: 42, Interface: "wg-test", Session: "private-session", Generation: 1},
		Tuple:      ni.Tuple{Source: netip.MustParseAddrPort("10.0.0.2:1234"), Destination: netip.MustParseAddrPort(destination), Protocol: "tcp"},
		At:         now,
		TCPQuality: &ni.TCPFlowQuality{UploadACK: ni.LatencyMilliseconds{Samples: samples, Mean: &mean}, Retransmissions: retransmissions},
	}}
}

func TestNetworkHistoryUsesObservedTimeAndWeightedSamples(t *testing.T) {
	now := time.Now().UTC()
	flows := []ni.Flow{historyTestFlow(now, "192.0.2.1:443", 1, 100, 2), historyTestFlow(now, "192.0.2.2:443", 3, 20, 4)}
	var sampler networkHistorySampler
	first := sampler.Snapshot(flows, "epoch", now)
	if len(first.Points) != 1 || first.Points[0].ACKRTT == nil || *first.Points[0].ACKRTT != 40 || !first.Points[0].At.Equal(now) || first.Points[0].RetransmissionDelta != nil {
		t.Fatalf("first=%+v", first)
	}
	if got := sampler.Snapshot(flows, "epoch", now.Add(10*time.Second)); len(got.Points) != 1 {
		t.Fatalf("poll fabricated history=%+v", got)
	}
	for i := range flows {
		flows[i].At = now.Add(time.Second)
	}
	if got := sampler.Snapshot(flows, "epoch", now.Add(time.Second)); len(got.Points) != 1 {
		t.Fatalf("downsampling admitted another point=%+v", got)
	}
	for i := range flows {
		flows[i].At = now.Add(6 * time.Second)
	}
	flows[0].TCPQuality.Retransmissions++
	second := sampler.Snapshot(flows, "epoch", now.Add(6*time.Second))
	if len(second.Points) != 2 || second.Points[0].RetransmissionDelta == nil || *second.Points[0].RetransmissionDelta != 1 {
		t.Fatalf("second=%+v", second)
	}
	// A changed tuple set cannot compare cumulative counters with the old set.
	flows = flows[:1]
	flows[0].At = now.Add(12 * time.Second)
	changed := sampler.Snapshot(flows, "epoch", now.Add(12*time.Second))
	if changed.Points[0].RetransmissionDelta != nil {
		t.Fatal("membership change preserved delta")
	}
	raw, _ := json.Marshal(changed)
	if strings.Contains(string(raw), "192.0.2") || strings.Contains(string(raw), "private-session") {
		t.Fatal("private flow identity retained in history")
	}
}

func TestNetworkHistoryUnavailableExpiryAndResponseBounds(t *testing.T) {
	now := time.Now().UTC()
	var sampler networkHistorySampler
	if points := sampler.Snapshot(nil, "epoch", now).Points; len(points) != 0 {
		t.Fatal(points)
	}
	for _, at := range []time.Time{now.Add(time.Second), now.Add(-2 * time.Minute)} {
		invalid := historyTestFlow(at, "192.0.2.1:443", 1, 20, 0)
		if got := sampler.Snapshot([]ni.Flow{invalid}, "epoch", now); len(got.Points) != 0 {
			t.Fatalf("unavailable observation created history=%+v", got)
		}
	}
	flow := historyTestFlow(now, "192.0.2.1:443", 0, 0, 0)
	flow.TCPQuality = nil
	got := sampler.Snapshot([]ni.Flow{flow}, "epoch", now)
	if len(got.Points) != 1 || got.Points[0].ACKRTT != nil || got.Points[0].Jitter != nil {
		t.Fatal(got)
	}
	if got := sampler.Snapshot(nil, "epoch", now.Add(31*time.Minute)); len(got.Points) != 0 {
		t.Fatal(got)
	}
	flows := make([]ni.Flow, 200)
	for i := range flows {
		flows[i] = historyTestFlow(now, "192.0.2.1:443", 1, 20, 0)
		flows[i].Binding.UserID = int64(i + 1)
	}
	got = sampler.Snapshot(flows, "epoch", now)
	if !got.Truncated || len(got.Points) != 128 {
		t.Fatalf("bounded=%d truncated=%t", len(got.Points), got.Truncated)
	}
}
