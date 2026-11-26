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
	mu       sync.Mutex
	tracker  *Tracker
	bindings map[netip.Addr]Binding
	flows    map[Tuple]Observation
	capacity int
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
	stopped  bool
}

func NewSessionCapture(tracker *Tracker, bindings []Binding, capacity int) (*SessionCapture, error) {
	if tracker == nil || capacity < 1 || len(bindings) == 0 || len(bindings) > capacity {
		return nil, errors.New("tracker, bounded capacity and bindings required")
	}
	c := &SessionCapture{tracker: tracker, bindings: make(map[netip.Addr]Binding), flows: make(map[Tuple]Observation), capacity: capacity}
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
func (c *SessionCapture) Consume(p CapturedPacket) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || p.Metadata.WireLength == 0 || p.At.IsZero() {
		return false
	}
	tuple := p.Metadata.Tuple
	address := tuple.Source.Addr()
	if p.Outgoing {
		tuple.Source, tuple.Destination = tuple.Destination, tuple.Source
		address = tuple.Source.Addr()
	}
	b, ok := c.bindings[address]
	if !ok {
		return false
	}
	o, exists := c.flows[tuple]
	if !exists {
		if p.Outgoing || len(c.flows) >= c.capacity {
			return false
		}
		o = Observation{CollectorEpoch: c.tracker.Epoch(), Binding: b, Tuple: tuple}
	}
	if p.At.Before(o.At) || o.Sequence == math.MaxUint64 {
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
	o.At = p.At
	if !c.tracker.Observe(o, time.Now().UTC()) {
		return false
	}
	c.flows[tuple] = o
	return true
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
	c.done = make(chan struct{})
	done := c.done
	ready := make(chan struct{})
	go func() {
		err := runCapture(ctx, cfg, func(p CapturedPacket) { c.Consume(p) }, func() { close(ready) })
		c.mu.Lock()
		c.err = err
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
