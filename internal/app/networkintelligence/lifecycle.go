package networkintelligence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// CaptureSession must join its receive loop before Stop returns, including on error.
type CaptureSession interface {
	Start(context.Context, CaptureConfig) error
	Stop() error
}

type managedCapture struct {
	session  CaptureSession
	bindings []Binding
}

// Lifecycle owns observation sessions only. A caller must put every kernel
// ownership mutation through Mutate and verify applied state before Publish.
// A single lock deliberately serializes publication, revocation and mutation.
type Lifecycle struct {
	enabled  atomic.Bool
	mu       sync.Mutex
	tracker  *Tracker
	capacity int
	sessions map[string]managedCapture
}

func NewLifecycle(tracker *Tracker, capacity int) (*Lifecycle, error) {
	if tracker == nil || capacity < 1 {
		return nil, errors.New("tracker and positive session capacity required")
	}
	return &Lifecycle{tracker: tracker, capacity: capacity, sessions: make(map[string]managedCapture)}, nil
}

func (l *Lifecycle) Enabled() bool { return l.enabled.Load() }

// SetEnabled(false) closes and joins all capture sockets before returning.
// Enabling never starts a collector without an explicit verified publication.
func (l *Lifecycle) SetEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled.Store(enabled)
	if !enabled {
		for name := range l.sessions {
			l.revoke(name)
		}
	}
}

// Publish takes ownership of session and a copy of the verified bindings.
// A failed publication revokes its bindings and closes its session.
func (l *Lifecycle) Publish(ctx context.Context, cfg CaptureConfig, bindings []Binding, factory func() (CaptureSession, error)) error {
	if factory == nil || len(bindings) == 0 {
		return errors.New("session and verified bindings required")
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	for _, b := range bindings {
		if b.Interface != cfg.Interface {
			return errors.New("binding interface mismatch")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled.Load() {
		return errors.New("monitoring disabled")
	}
	if _, exists := l.sessions[cfg.Interface]; exists {
		return errors.New("capture must be revoked before replacement")
	}
	if len(l.sessions) >= l.capacity {
		return errors.New("capture session capacity reached")
	}
	owned := managedCapture{}
	l.sessions[cfg.Interface] = owned
	for _, b := range bindings {
		l.tracker.mu.Lock()
		old, exists := l.tracker.bindings[key(b)]
		l.tracker.mu.Unlock()
		if exists && old.Session != "" {
			l.revoke(cfg.Interface)
			return errors.New("binding already owned")
		}
		if err := l.tracker.Bind(b); err != nil {
			l.revoke(cfg.Interface)
			return err
		}
		owned.bindings = append(owned.bindings, b)
		l.sessions[cfg.Interface] = owned
	}
	session, err := factory()
	if err != nil {
		l.revoke(cfg.Interface)
		return err
	}
	if session == nil {
		l.revoke(cfg.Interface)
		return errors.New("capture factory returned nil")
	}
	owned.session = session
	l.sessions[cfg.Interface] = owned
	if err := session.Start(ctx, cfg); err != nil {
		l.revoke(cfg.Interface)
		return err
	}
	return nil
}

// Mutate closes the old capture and revokes its binding before running fn.
// Capture errors cannot prevent the authorized VPN mutation. No automatic
// restart occurs, even when fn fails. Publish requires fresh verification.
func (l *Lifecycle) Mutate(interfaceName string, fn func() error) error {
	if fn == nil {
		return errors.New("mutation function required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revoke(interfaceName)
	return fn()
}

func (l *Lifecycle) revoke(name string) {
	owned, ok := l.sessions[name]
	if !ok {
		return
	}
	if owned.session != nil {
		_ = owned.session.Stop()
	}
	for _, b := range owned.bindings {
		l.tracker.Unbind(b)
	}
	delete(l.sessions, name)
}
