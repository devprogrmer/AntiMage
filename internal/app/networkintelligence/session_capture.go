package networkintelligence

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"sync"
	"time"
)

// SessionCapture consumes one capture socket with an immutable, externally
// verified binding snapshot. The caller must close and join the old capture
// before changing kernel ownership. This adapter cannot verify that contract.
type SessionCapture struct {
	mu         sync.Mutex
	tracker    *Tracker
	bindings   map[netip.Addr]Binding
	flows      map[Tuple]Observation
	quality    map[Tuple]*capturedTCPQuality
	statistics CaptureStatistics
	capacity   int
	cancel     context.CancelFunc
	done       chan struct{}
	err        error
	stopped    bool
	dirty      map[Tuple]struct{}
}

type capturedTCPQuality struct {
	estimator *TCPQuality
	windows   [2]*QualityWindow
	result    TCPQualityResult
}

func NewSessionCapture(tracker *Tracker, bindings []Binding, capacity int) (*SessionCapture, error) {
	if tracker == nil || capacity < 1 || len(bindings) == 0 || len(bindings) > capacity {
		return nil, errors.New("tracker, bounded capacity and bindings required")
	}
	c := &SessionCapture{tracker: tracker, bindings: make(map[netip.Addr]Binding), flows: make(map[Tuple]Observation), quality: make(map[Tuple]*capturedTCPQuality), capacity: capacity, dirty: make(map[Tuple]struct{})}
	first := bindings[0]
	for _, b := range bindings {
		if b.NodeID != first.NodeID || b.Interface != first.Interface {
			return nil, errors.New("capture bindings must share node and interface")
		}
		if _, exists := c.bindings[b.Address]; exists {
			return nil, errors.New("ambiguous or duplicate address binding")
		}
		// Validate against the authority without publishing caller supplied data.
		tracker.mu.Lock()
		verified := tracker.bindings[key(b)] == b && b.Session != ""
		tracker.mu.Unlock()
		if !verified {
			return nil, errors.New("binding must already be published by authority")
		}
		c.bindings[b.Address] = b
	}
	return c, nil
}

// Consume counts each delivered observation once. Socket observation points
// must be selected so the same packet is not delivered twice. Downloads are
// admitted only after an upload established the tuple in this generation.
func (c *SessionCapture) Consume(p CapturedPacket) bool { return c.consume(p, false) }

func (c *SessionCapture) consume(p CapturedPacket, deferred bool) bool {
	return c.consumeAt(p, deferred, time.Now().UTC())
}

func (c *SessionCapture) consumeAt(p CapturedPacket, deferred bool, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || p.Metadata.WireLength == 0 || p.At.IsZero() || p.At.After(now) || !p.At.After(now.Add(-c.tracker.timeout)) {
		return false
	}
	tuple := p.Metadata.Tuple
	address := tuple.Source.Addr()
	if p.Outgoing {
		tuple.Source, tuple.Destination = tuple.Destination, tuple.Source
		address = tuple.Source.Addr()
	}
	b, ok := c.bindings[address]
	if !ok || p.At.Before(b.Since) {
		return false
	}
	o, exists := c.flows[tuple]
	if !exists {
		if p.Outgoing || len(c.flows) >= c.capacity {
			return false
		}
		o = Observation{CollectorEpoch: c.tracker.Epoch(), Binding: b, Tuple: tuple}
	}
	if o.Sequence == math.MaxUint64 {
		return false
	}
	bytes := uint64(p.Metadata.WireLength)
	if p.Outgoing {
		if o.DownloadPackets == math.MaxUint64 || o.DownloadBytes > math.MaxUint64-bytes {
			return false
		}
		o.DownloadPackets++
		o.DownloadBytes += bytes
	} else {
		if o.UploadPackets == math.MaxUint64 || o.UploadBytes > math.MaxUint64-bytes {
			return false
		}
		o.UploadPackets++
		o.UploadBytes += bytes
	}
	o.Sequence++
	reordered := p.At.Before(o.At)
	if !reordered {
		o.At = p.At
	}
	if reordered {
		delete(c.quality, tuple)
		o.TCPQuality = nil
	} else if tuple.Protocol == "tcp" {
		o.TCPQuality = c.observeTCP(tuple, p, deferred)
	}
	if !deferred && !c.tracker.Observe(o, now) {
		return false
	}
	c.flows[tuple] = o
	if deferred {
		c.dirty[tuple] = struct{}{}
	}
	return true
}

// flush publishes cumulative counters once per observed kernel block rather
// than acquiring the global tracker and hashing the same flow twice per packet.
func (c *SessionCapture) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	for tuple := range c.dirty {
		observation := c.flows[tuple]
		if q := c.quality[tuple]; q != nil {
			observation.TCPQuality = q.snapshot(observation.At)
			c.flows[tuple] = observation
		}
		if !c.stopped && !c.tracker.Observe(observation, now) {
			delete(c.flows, tuple)
			delete(c.quality, tuple)
		}
		delete(c.dirty, tuple)
	}
}

func milliseconds(s QualitySummary) LatencyMilliseconds {
	convert := func(d *time.Duration) *float64 {
		if d == nil {
			return nil
		}
		v := float64(*d) / float64(time.Millisecond)
		return &v
	}
	return LatencyMilliseconds{s.Samples, convert(s.Min), convert(s.Mean), convert(s.Max), convert(s.P50), convert(s.P95), convert(s.P99), convert(s.Jitter)}
}

func (c *SessionCapture) observeTCP(tuple Tuple, packet CapturedPacket, deferred bool) *TCPFlowQuality {
	q := c.quality[tuple]
	previous := c.flows[tuple]
	if q == nil || packet.At.Sub(previous.At) >= c.tracker.timeout {
		estimator, _ := NewTCPQuality(32)
		up, _ := NewQualityWindow(32, time.Minute)
		down, _ := NewQualityWindow(32, time.Minute)
		q = &capturedTCPQuality{estimator: estimator, windows: [2]*QualityWindow{up, down}}
		c.quality[tuple] = q
	}
	direction := 0
	if packet.Outgoing {
		direction = 1
	}
	result := q.estimator.Observe(packet.Metadata, direction, packet.At)
	// ACKs in one direction measure segments transmitted in the opposite leg.
	for _, rtt := range result.ACKRTT {
		q.windows[1-direction].Add(rtt, packet.At, packet.At)
	}
	result.ACKRTT = nil
	q.result = result
	if deferred {
		return nil
	}
	return q.snapshot(packet.At)
}

func (q *capturedTCPQuality) snapshot(at time.Time) *TCPFlowQuality {
	result := q.result
	return &TCPFlowQuality{Method: "tcp_ack_observation", UploadACK: milliseconds(q.windows[0].Snapshot(at)), DownloadACK: milliseconds(q.windows[1].Snapshot(at)), Retransmissions: result.ObservedRetransmissions, DuplicateACKs: result.ObservedDuplicateACKs, DiscardedSegments: result.DiscardedSegments}
}

// Start starts a single socket. Stop cancels and joins it before returning.
func (c *SessionCapture) Start(ctx context.Context, cfg CaptureConfig) error {
	c.mu.Lock()
	if c.done != nil || c.stopped {
		c.mu.Unlock()
		return errors.New("capture already started or stopped")
	}
	for _, b := range c.bindings {
		if cfg.Interface != b.Interface {
			c.mu.Unlock()
			return errors.New("capture interface does not match binding")
		}
	}
	ctx, c.cancel = context.WithCancel(ctx)
	cfg.Statistics = c.captureStatisticsCallback(cfg.Statistics)
	cfg.afterBatch = c.flush
	cfg.consumeTimed = func(p CapturedPacket, now time.Time) { c.consumeAt(p, true, now) }
	c.done = make(chan struct{})
	done := c.done
	ready := make(chan struct{})
	go func() {
		err := runCapture(ctx, cfg, func(p CapturedPacket) { c.consume(p, true) }, func() { close(ready) })
		c.mu.Lock()
		c.err = err
		c.stopped = true
		// Once capture ends, revoke only this generation. Late telemetry and
		// previously published flows cannot imply continued observation.
		for _, binding := range c.bindings {
			c.tracker.Unbind(binding)
		}
		clear(c.flows)
		clear(c.quality)
		clear(c.dirty)
		c.mu.Unlock()
		close(done)
	}()
	c.mu.Unlock()
	select {
	case <-ready:
		if err := ctx.Err(); err != nil {
			c.Stop()
			return err
		}
		return nil
	case <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	}
}

func (c *SessionCapture) captureStatisticsCallback(next func(CaptureStatistics)) func(CaptureStatistics) {
	return func(s CaptureStatistics) {
		c.observeStatistics(s)
		if next != nil {
			next(s)
		}
	}
}

// A reported capture gap invalidates outstanding timing and prior summaries.
// Transport byte counters remain cumulative and cannot establish packet loss.
func (c *SessionCapture) observeStatistics(s CaptureStatistics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gap := s.BudgetExceeded || s.KernelDrops != c.statistics.KernelDrops || s.RejectedPackets != c.statistics.RejectedPackets
	c.statistics = s
	if !gap {
		return
	}
	clear(c.quality)
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	for tuple, observation := range c.flows {
		observation.TCPQuality = nil
		c.flows[tuple] = observation
		key := flowKey{key(observation.Binding), observation.Binding.Generation, tuple}
		if flow, ok := c.tracker.flows[key]; ok && flow.Binding == observation.Binding {
			flow.TCPQuality = nil
			c.tracker.flows[key] = flow
		}
		if highWater, ok := c.tracker.highWater[key]; ok && highWater.Binding == observation.Binding {
			highWater.TCPQuality = nil
			c.tracker.highWater[key] = highWater
		}
	}
}

func (c *SessionCapture) Stop() error {
	c.mu.Lock()
	c.stopped = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
