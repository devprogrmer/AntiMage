package nodeagent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	errRuntimeStopping = errors.New("runtime is stopping")
	errRuntimeStopped  = errors.New("runtime is stopped")
)

// runtimeLifecycle provides an admission barrier for Stop and stable FIFO
// coordination gates for each inbound. Its mutex is never held during runtime
// work, so independent inbounds remain concurrent.
type runtimeLifecycle struct {
	mu       sync.Mutex
	gates    map[string]*lifecycleGate
	active   int
	changed  chan struct{}
	stopCh   chan struct{}
	stopping bool
	stopped  bool
	stopBusy bool
}

func newRuntimeLifecycle() *runtimeLifecycle {
	return &runtimeLifecycle{gates: make(map[string]*lifecycleGate), changed: make(chan struct{}), stopCh: make(chan struct{})}
}

func normalizeLifecycleTags(tags []string) []string {
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			set[tag] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for tag := range set {
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func (l *runtimeLifecycle) begin(allowStopped bool) (func(), <-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopping {
		return nil, nil, errRuntimeStopping
	}
	if l.stopped && !allowStopped {
		return nil, nil, errRuntimeStopped
	}
	l.active++
	stopCh := l.stopCh
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.active--
			close(l.changed)
			l.changed = make(chan struct{})
			l.mu.Unlock()
		})
	}, stopCh, nil
}

func (l *runtimeLifecycle) gateLocked(tag string) *lifecycleGate {
	gate := l.gates[tag]
	if gate == nil {
		gate = &lifecycleGate{}
		l.gates[tag] = gate
	}
	return gate
}

func (l *runtimeLifecycle) gate(tag string) *lifecycleGate {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gateLocked(tag)
}

func (l *runtimeLifecycle) lockTags(ctx context.Context, tags []string, includeKnown bool, stopCh <-chan struct{}) (func(), error) {
	tags = normalizeLifecycleTags(tags)
	l.mu.Lock()
	if includeKnown {
		for tag := range l.gates {
			tags = append(tags, tag)
		}
	}
	tags = normalizeLifecycleTags(tags)
	gates := make([]*lifecycleGate, len(tags))
	for i, tag := range tags {
		gates[i] = l.gateLocked(tag)
	}
	l.mu.Unlock()
	releases := make([]func(), 0, len(tags))
	for _, gate := range gates {
		release, err := gate.acquireWithStop(ctx, stopCh)
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, nil
}

func (l *runtimeLifecycle) runMutation(ctx context.Context, inboundTag string, fn func() error) error {
	end, stopCh, err := l.begin(false)
	if err != nil {
		return err
	}
	defer end()
	release, err := l.lockTags(ctx, []string{inboundTag}, false, stopCh)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.checkStart(stopCh); err != nil {
		return err
	}
	return fn()
}

func (l *runtimeLifecycle) checkStart(stopCh <-chan struct{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-stopCh:
		return errRuntimeStopping
	default:
	}
	if l.stopping {
		return errRuntimeStopping
	}
	return nil
}

func (l *runtimeLifecycle) runApply(ctx context.Context, inboundTags []string, fn func() error) error {
	end, stopCh, err := l.begin(true)
	if err != nil {
		return err
	}
	defer end()
	release, err := l.lockTags(ctx, inboundTags, true, stopCh)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.checkStart(stopCh); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	l.mu.Lock()
	if !l.stopping {
		l.stopped = false
	}
	l.mu.Unlock()
	return nil
}

func (l *runtimeLifecycle) runStop(ctx context.Context, fn func() error) error {
	l.mu.Lock()
	if l.stopBusy {
		l.mu.Unlock()
		return errRuntimeStopping
	}
	if !l.stopping {
		l.stopping = true
		close(l.stopCh)
	}
	l.stopBusy = true
	for l.active > 0 {
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			l.mu.Lock()
			// Keep admission closed until a later Stop call drains active work.
			l.stopBusy = false
			l.mu.Unlock()
			return ctx.Err()
		case <-changed:
		}
		l.mu.Lock()
	}
	tags := make([]string, 0, len(l.gates))
	for tag := range l.gates {
		tags = append(tags, tag)
	}
	l.mu.Unlock()
	release, err := l.lockTags(ctx, tags, true, nil)
	if err != nil {
		l.mu.Lock()
		// A canceled barrier remains fail-closed and can be completed by retry.
		l.stopBusy = false
		l.mu.Unlock()
		return err
	}
	callbackErr := fn()
	release()
	l.mu.Lock()
	// A failed Stop leaves runtime state uncertain, so keep mutation admission closed.
	l.stopped, l.stopping = true, false
	l.stopBusy = false
	l.stopCh = make(chan struct{})
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
	return callbackErr
}
