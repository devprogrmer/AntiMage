// Package networkintelligence tracks metadata supplied by verified collectors.
package networkintelligence

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"sync"
	"time"
)

// Binding identifies an authenticated lease. Generation must change whenever
// ownership changes, including reconnects. Collectors must obtain it at capture.
type Binding struct {
	NodeID     int64
	Interface  string
	Address    netip.Addr
	UserID     int64
	Session    string
	Generation uint64
	Since      time.Time
}

type Tuple struct {
	Source      netip.AddrPort
	Destination netip.AddrPort
	Protocol    string
}

// Observation uses cumulative directional counters from one collector epoch.
// Sequence must increase for every observation within a binding generation.
type Observation struct {
	CollectorEpoch                 string
	Binding                        Binding
	Tuple                          Tuple
	Sequence                       uint64
	At                             time.Time
	UploadPackets, DownloadPackets uint64
	UploadBytes, DownloadBytes     uint64
}

type Flow struct{ Observation }
type leaseKey struct {
	node    int64
	iface   string
	address netip.Addr
}
type flowKey struct {
	lease      leaseKey
	generation uint64
	tuple      Tuple
}

type Tracker struct {
	epoch     string
	mu        sync.Mutex
	capacity  int
	timeout   time.Duration
	bindings  map[leaseKey]Binding
	flows     map[flowKey]Flow
	highWater map[flowKey]Observation
}

func New(capacity int, timeout time.Duration) (*Tracker, error) {
	if capacity < 1 || timeout <= 0 {
		return nil, errors.New("positive capacity and timeout required")
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, err
	}
	return &Tracker{epoch: hex.EncodeToString(epoch[:]), capacity: capacity, timeout: timeout, bindings: make(map[leaseKey]Binding), flows: make(map[flowKey]Flow), highWater: make(map[flowKey]Observation)}, nil
}

// Epoch is negotiated with collectors before accepting their observations.
// A new Tracker rejects all telemetry captured for a previous instance.
func (t *Tracker) Epoch() string { return t.epoch }

func key(b Binding) leaseKey { return leaseKey{b.NodeID, b.Interface, b.Address} }

// Bind accepts monotonically increasing ownership epochs, bounded by capacity.
// Rebinding immediately discards metadata belonging to the previous owner.
func (t *Tracker) Bind(b Binding) error {
	if b.NodeID <= 0 || b.UserID <= 0 || b.Interface == "" || b.Session == "" || b.Generation == 0 || !b.Address.IsValid() || b.Since.IsZero() {
		return errors.New("invalid binding")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	k := key(b)
	if old, ok := t.bindings[k]; ok {
		if old == b {
			return nil
		}
		if b.Generation <= old.Generation || b.Since.Before(old.Since) {
			return errors.New("stale binding")
		}
	} else if len(t.bindings) >= t.capacity {
		return errors.New("binding capacity reached")
	}
	for fk := range t.flows {
		if fk.lease == k {
			delete(t.flows, fk)
		}
	}
	t.bindings[k] = b
	for fk := range t.highWater {
		if fk.lease == k {
			delete(t.highWater, fk)
		}
	}
	return nil
}

// Observe returns false for unattributed, stale, duplicate or over-capacity data.
// It never infers a session from an address. A missing generation is rejected.
func (t *Tracker) Observe(o Observation, now time.Time) bool {
	if o.CollectorEpoch != t.epoch {
		return false
	}
	if o.Tuple.Protocol != "tcp" && o.Tuple.Protocol != "udp" {
		return false
	}
	if !o.Tuple.Source.IsValid() || !o.Tuple.Destination.IsValid() || o.Tuple.Source.Addr() != o.Binding.Address || o.Sequence == 0 || o.At.After(now) || !o.At.After(now.Add(-t.timeout)) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expire(now)
	k := key(o.Binding)
	b, ok := t.bindings[k]
	if !ok || b != o.Binding || o.At.Before(b.Since) {
		return false
	}
	fk := flowKey{k, b.Generation, o.Tuple}
	if old, ok := t.highWater[fk]; ok {
		if o.Sequence <= old.Sequence || o.At.Before(old.At) || o.UploadPackets < old.UploadPackets || o.DownloadPackets < old.DownloadPackets || o.UploadBytes < old.UploadBytes || o.DownloadBytes < old.DownloadBytes {
			return false
		}
	} else if len(t.highWater) >= t.capacity {
		return false
	}
	t.flows[fk] = Flow{o}
	t.highWater[fk] = o
	return true
}

func (t *Tracker) expire(now time.Time) {
	for k, f := range t.flows {
		if !f.At.After(now.Add(-t.timeout)) {
			delete(t.flows, k)
		}
	}
}

func (t *Tracker) Snapshot(now time.Time) []Flow {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expire(now)
	result := make([]Flow, 0, len(t.flows))
	for _, f := range t.flows {
		result = append(result, f)
	}
	return result
}

// Unbind retains the epoch tombstone so late binding updates cannot revive it.
func (t *Tracker) Unbind(b Binding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := key(b)
	if t.bindings[k] != b {
		return
	}
	tombstone := b
	tombstone.Session = ""
	t.bindings[k] = tombstone
	for fk := range t.flows {
		if fk.lease == k {
			delete(t.flows, fk)
		}
	}
}
