package nodeagent

import (
	"context"
	"sync"
)

type lifecycleWaiter struct {
	ready chan struct{}
	state uint8 // queued, granted, or canceled/released
}

// lifecycleGate is a FIFO lock for one stable inbound key. Cancellation and
// ownership transfer are resolved under the same mutex.
type lifecycleGate struct {
	mu      sync.Mutex
	held    bool
	waiters []*lifecycleWaiter
}

func (g *lifecycleGate) acquire(ctx context.Context) (func(), error) {
	return g.acquireWithStop(ctx, nil)
}

func (g *lifecycleGate) acquireWithStop(ctx context.Context, stopCh <-chan struct{}) (func(), error) {
	w := &lifecycleWaiter{ready: make(chan struct{})}
	g.mu.Lock()
	if !g.held {
		g.held, w.state = true, 1
		g.mu.Unlock()
		return g.finishAcquire(ctx, stopCh, w)
	}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()
	select {
	case <-w.ready:
		return g.finishAcquire(ctx, stopCh, w)
	case <-stopCh:
		return g.finishCanceled(w, errRuntimeStopping)
	case <-ctx.Done():
		return g.finishCanceled(w, ctx.Err())
	}
}

func (g *lifecycleGate) finishCanceled(w *lifecycleWaiter, err error) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.state == 0 {
		for i, queued := range g.waiters {
			if queued == w {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				break
			}
		}
		w.state = 2
	} else if w.state == 1 {
		g.handoffLocked(w)
	}
	return nil, err
}

func (g *lifecycleGate) finishAcquire(ctx context.Context, stopCh <-chan struct{}, w *lifecycleWaiter) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.state != 1 {
		return nil, ctx.Err()
	}
	select {
	case <-stopCh:
		g.handoffLocked(w)
		return nil, errRuntimeStopping
	default:
	}
	if err := ctx.Err(); err != nil {
		g.handoffLocked(w)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if w.state == 1 {
				g.handoffLocked(w)
			}
		})
	}, nil
}

func (g *lifecycleGate) handoffLocked(owner *lifecycleWaiter) {
	owner.state = 2
	for len(g.waiters) != 0 {
		next := g.waiters[0]
		g.waiters = g.waiters[1:]
		if next.state != 0 {
			continue
		}
		next.state = 1
		close(next.ready)
		return
	}
	g.held = false
}
