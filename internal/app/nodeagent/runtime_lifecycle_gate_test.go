package nodeagent

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func waitGateQueue(t *testing.T, g *lifecycleGate, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		got := len(g.waiters)
		g.mu.Unlock()
		if got == count {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("gate queue length did not become %d", count)
}

func TestLifecycleGateFIFOAndCanceledMiddleWaiter(t *testing.T) {
	g := &lifecycleGate{}
	owner, err := g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order := make(chan int, 2)
	start := func(id int, ctx context.Context) <-chan error {
		done := make(chan error, 1)
		go func() {
			release, err := g.acquire(ctx)
			if err == nil {
				order <- id
				release()
			}
			done <- err
		}()
		return done
	}
	one := start(1, context.Background())
	waitGateQueue(t, g, 1)
	canceledCtx, cancel := context.WithCancel(context.Background())
	two := start(2, canceledCtx)
	waitGateQueue(t, g, 2)
	three := start(3, context.Background())
	waitGateQueue(t, g, 3)
	cancel()
	if err := <-two; !errors.Is(err, context.Canceled) {
		t.Fatalf("middle waiter error = %v", err)
	}
	owner()
	if got := <-order; got != 1 {
		t.Fatalf("first owner = %d", got)
	}
	if err := <-one; err != nil {
		t.Fatal(err)
	}
	if got := <-order; got != 3 {
		t.Fatalf("third owner = %d", got)
	}
	if err := <-three; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || len(g.waiters) != 0 {
		t.Fatalf("gate leaked state: held=%v waiters=%d", g.held, len(g.waiters))
	}
}

func TestLifecycleGateCancelRacingHandoffNeverHasTwoOwners(t *testing.T) {
	for i := 0; i < 100; i++ {
		g := &lifecycleGate{}
		owner, err := g.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var active, overlap atomic.Int32
		firstEntered, releaseFirst := make(chan struct{}), make(chan struct{})
		secondEntered := make(chan struct{})
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		var released atomic.Bool
		release := func() {
			if released.CompareAndSwap(false, true) {
				close(releaseFirst)
			}
		}
		first := func() {
			r, e := g.acquire(ctx)
			if e == nil {
				if active.Add(1) != 1 {
					overlap.Add(1)
				}
				close(firstEntered)
				<-releaseFirst
				active.Add(-1)
				r()
			}
			firstDone <- e
		}
		second := func() {
			r, e := g.acquire(context.Background())
			if e == nil {
				if active.Add(1) != 1 {
					overlap.Add(1)
				}
				close(secondEntered)
				active.Add(-1)
				r()
			}
			secondDone <- e
		}
		go first()
		waitGateQueue(t, g, 1)
		go second()
		waitGateQueue(t, g, 2)
		startRace := make(chan struct{})
		go func() { <-startRace; cancel() }()
		go func() { <-startRace; owner() }()
		close(startRace)
		select {
		case <-firstEntered:
			g.mu.Lock()
			secondGranted := len(g.waiters) > 0 && g.waiters[0].state == 1
			g.mu.Unlock()
			if secondGranted {
				release()
				t.Fatal("gate handed off while first owner was active")
			}
		case <-secondEntered:
		case <-time.After(time.Second):
			release()
			t.Fatal("cancel/handoff deadlocked")
		}
		release()
		if err := <-firstDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		select {
		case <-secondEntered:
		case <-time.After(time.Second):
			t.Fatal("next waiter did not acquire")
		}
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if overlap.Load() != 0 {
			t.Fatal("two callbacks owned the same inbound simultaneously")
		}
		g.mu.Lock()
		leaked := g.held || len(g.waiters) != 0
		g.mu.Unlock()
		if leaked {
			t.Fatal("handoff leaked gate ownership")
		}
	}
}
