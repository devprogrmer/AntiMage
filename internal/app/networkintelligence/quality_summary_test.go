package networkintelligence

import (
	"testing"
	"time"
)

func TestQualityWindowBoundsPercentilesAndExpiry(t *testing.T) {
	now := time.Now()
	q, err := NewQualityWindow(3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i, ms := range []int{1000, 10, 30, 20} {
		if !q.Add(time.Duration(ms)*time.Millisecond, now.Add(time.Duration(i)*time.Second), now.Add(4*time.Second)) {
			t.Fatal("valid sample rejected")
		}
	}
	s := q.Snapshot(now.Add(4 * time.Second))
	if s.Method != "tcp_ack_observation" || s.Samples != 3 || *s.Min != 10*time.Millisecond || *s.Max != 30*time.Millisecond || *s.Mean != 20*time.Millisecond || *s.P50 != 20*time.Millisecond || *s.P95 != 30*time.Millisecond || *s.P99 != 30*time.Millisecond || *s.Jitter != 15*time.Millisecond {
		t.Fatalf("summary=%+v", s)
	}
	if s := q.Snapshot(now.Add(2 * time.Minute)); s.Samples != 0 || s.P50 != nil || s.Jitter != nil {
		t.Fatalf("expired summary=%+v", s)
	}
}

func TestQualityWindowRejectsInvalidSamples(t *testing.T) {
	now := time.Now()
	q, _ := NewQualityWindow(2, time.Minute)
	for _, sample := range []latencySample{{now, 0}, {now, -time.Second}, {now.Add(time.Second), time.Millisecond}, {now.Add(-time.Minute), time.Millisecond}, {time.Time{}, time.Millisecond}} {
		if q.Add(sample.rtt, sample.at, now) {
			t.Fatal("invalid sample accepted")
		}
	}
	q.Add(time.Millisecond, now, now)
	if q.Add(time.Millisecond, now.Add(-time.Second), now) {
		t.Fatal("out of order sample accepted")
	}
	if s := q.Snapshot(now); s.Samples != 1 || s.Jitter != nil {
		t.Fatalf("single sample=%+v", s)
	}
}

func BenchmarkQualityWindowAdd(b *testing.B) {
	q, _ := NewQualityWindow(256, time.Minute)
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q.Add(20*time.Millisecond, now, now)
	}
}
