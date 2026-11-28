package networkintelligence

import (
	"testing"
	"time"
)

func TestHistoryBoundsRetentionAndCounterResets(t *testing.T) {
	now := time.Now()
	h, err := NewQualityHistory(HistoryLimits{MaxSamples: 2, MaxNodes: 1, MaxUsersPerNode: 1, SamplesPerUser: 2, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	in := HistoryInput{NodeID: 1, UserID: 2, Epoch: "a", At: now, Retransmissions: 100}
	if !h.Add(in, now) || h.Add(in, now) {
		t.Fatal("first or duplicate")
	}
	in.At = now.Add(time.Second)
	in.Retransmissions = 102
	if !h.Add(in, in.At) {
		t.Fatal("second")
	}
	got := h.Snapshot(1, 2, in.At)
	if got[0].RetransmissionDelta != nil || *got[1].RetransmissionDelta != 2 || len(got[1].Findings) != 1 {
		t.Fatal(got)
	}
	*got[1].RetransmissionDelta = 999
	if *h.Snapshot(1, 2, in.At)[1].RetransmissionDelta != 2 {
		t.Fatal("snapshot aliases state")
	}
	in.At = in.At.Add(time.Second)
	in.Retransmissions = 1
	if !h.Add(in, in.At) {
		t.Fatal("reset")
	}
	got = h.Snapshot(1, 2, in.At)
	if len(got) != 2 || got[1].RetransmissionDelta != nil || len(got[1].Findings) != 0 {
		t.Fatal(got)
	}
	other := in
	other.NodeID = 3
	if h.Add(other, other.At) {
		t.Fatal("node cap")
	}
	other = in
	other.UserID = 3
	if h.Add(other, other.At) {
		t.Fatal("user cap")
	}
	if len(h.Snapshot(1, 2, now.Add(2*time.Minute))) != 0 {
		t.Fatal("retention")
	}
}

func TestHistoryEpochAndTimestampIsolation(t *testing.T) {
	now := time.Now()
	h, _ := NewQualityHistory(HistoryLimits{10, 2, 2, 3, time.Minute})
	in := HistoryInput{NodeID: 1, UserID: 1, Epoch: "old", At: now, CollectorDrops: 200}
	if !h.Add(in, now) {
		t.Fatal("initial")
	}
	in.At = now.Add(time.Second)
	in.Epoch = "new"
	in.CollectorDrops = 300
	if !h.Add(in, in.At) || h.Snapshot(1, 1, in.At)[1].CollectorDropDelta != nil {
		t.Fatal("epoch delta")
	}
	in.At = now.Add(-time.Second)
	if h.Add(in, now) {
		t.Fatal("out of order")
	}
	in.At = now.Add(time.Hour)
	if h.Add(in, now) {
		t.Fatal("future")
	}
}
