package networkintelligence

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

type latencySample struct {
	at  time.Time
	rtt time.Duration
}

// QualitySummary describes TCP ACK timing at the capture observation point.
// Nil durations mean no valid samples. Packet loss has no inferred value.
type QualitySummary struct {
	Method         string `json:"method"`
	Samples        int    `json:"samples"`
	Min, Mean, Max *time.Duration
	P50, P95, P99  *time.Duration
	// Jitter is the mean absolute difference between consecutive valid RTTs.
	Jitter *time.Duration
}

type QualityWindow struct {
	mu          sync.Mutex
	samples     []latencySample
	next, count int
	window      time.Duration
	last        time.Time
}

func NewQualityWindow(capacity int, window time.Duration) (*QualityWindow, error) {
	if capacity < 1 || capacity > 65536 || window <= 0 {
		return nil, errors.New("bounded positive capacity and window required")
	}
	return &QualityWindow{samples: make([]latencySample, capacity), window: window}, nil
}

func (q *QualityWindow) Add(rtt time.Duration, at, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if rtt <= 0 || at.IsZero() || at.After(now) || !at.After(now.Add(-q.window)) || at.Before(q.last) {
		return false
	}
	q.samples[q.next] = latencySample{at, rtt}
	q.next = (q.next + 1) % len(q.samples)
	q.count = min(q.count+1, len(q.samples))
	q.last = at
	return true
}

func (q *QualityWindow) Snapshot(now time.Time) QualitySummary {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := QualitySummary{Method: "tcp_ack_observation"}
	values := make([]time.Duration, 0, q.count)
	var mean, jitter float64
	var previous time.Duration
	start := (q.next - q.count + len(q.samples)) % len(q.samples)
	for i := 0; i < q.count; i++ {
		sample := q.samples[(start+i)%len(q.samples)]
		if !sample.at.After(now.Add(-q.window)) || sample.at.After(now) {
			continue
		}
		values = append(values, sample.rtt)
		mean += (float64(sample.rtt) - mean) / float64(len(values))
		if len(values) > 1 {
			difference := math.Abs(float64(sample.rtt) - float64(previous))
			jitter += (difference - jitter) / float64(len(values)-1)
		}
		previous = sample.rtt
	}
	result.Samples = len(values)
	if len(values) == 0 {
		return result
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	duration := func(value time.Duration) *time.Duration { return &value }
	percentile := func(percent float64) *time.Duration {
		return duration(values[int(math.Ceil(percent*float64(len(values))))-1])
	}
	result.Min, result.Max, result.Mean = duration(values[0]), duration(values[len(values)-1]), duration(time.Duration(mean))
	result.P50, result.P95, result.P99 = percentile(.50), percentile(.95), percentile(.99)
	if len(values) > 1 {
		result.Jitter = duration(time.Duration(jitter))
	}
	return result
}
