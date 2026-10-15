package nodeagent

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func waitLifecycleState(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("lifecycle state wait timed out")
}

func TestRuntimeLifecycleActiveAndWaitingMutationsDrainBeforeStop(t *testing.T) {
	l := newRuntimeLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- l.runMutation(context.Background(), "wg", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	waitingCalled := atomic.Bool{}
	waiting := make(chan error, 1)
	go func() {
		waiting <- l.runMutation(context.Background(), "wg", func() error { waitingCalled.Store(true); return nil })
	}()
	gate := l.gate("wg")
	waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 1 })
	stopCalled := atomic.Bool{}
	stopped := make(chan error, 1)
	go func() {
		stopped <- l.runStop(context.Background(), func() error { stopCalled.Store(true); return nil })
	}()
	waitLifecycleState(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.stopping })
	if stopCalled.Load() {
		t.Fatal("Stop overtook accepted mutation")
	}
	if err := l.runMutation(context.Background(), "other", func() error { return nil }); !errors.Is(err, errRuntimeStopping) {
		t.Fatalf("new mutation during Stop = %v", err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, errRuntimeStopping) {
			t.Fatalf("waiting mutation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel queued mutation")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if waitingCalled.Load() {
		t.Fatal("queued mutation ran after Stop began")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if !stopCalled.Load() {
		t.Fatal("Stop callback did not run")
	}
	if err := l.runMutation(context.Background(), "wg", func() error { return nil }); !errors.Is(err, errRuntimeStopped) {
		t.Fatalf("mutation after Stop = %v", err)
	}
}

func TestRuntimeLifecycleStopFailureIsFailClosedAndRetryable(t *testing.T) {
	l := newRuntimeLifecycle()
	failure := errors.New("stop failed")
	if err := l.runStop(context.Background(), func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("Stop error = %v", err)
	}
	if err := l.runMutation(context.Background(), "wg", func() error { return nil }); !errors.Is(err, errRuntimeStopped) {
		t.Fatalf("mutation after failed Stop = %v", err)
	}
	if err := l.runStop(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("repeat Stop: %v", err)
	}
}

func TestRuntimeLifecycleCanceledStopKeepsAdmissionClosedAndCanRetry(t *testing.T) {
	l := newRuntimeLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	active := make(chan error, 1)
	go func() {
		active <- l.runMutation(context.Background(), "wg", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.runStop(ctx, func() error { t.Fatal("timed out Stop callback ran"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out Stop = %v", err)
	}
	if err := l.runMutation(context.Background(), "other", func() error { return nil }); !errors.Is(err, errRuntimeStopping) {
		t.Fatalf("mutation admitted after canceled Stop = %v", err)
	}

	stopCalled := atomic.Bool{}
	stopped := make(chan error, 1)
	go func() {
		stopped <- l.runStop(context.Background(), func() error { stopCalled.Store(true); return nil })
	}()
	waitLifecycleState(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.stopBusy })
	if stopCalled.Load() {
		t.Fatal("retry Stop overtook active mutation")
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("retry Stop = %v", err)
	}
	if !stopCalled.Load() {
		t.Fatal("retry Stop callback did not run")
	}
}

func TestRuntimeLifecycleApplyConfigConcurrentWithStop(t *testing.T) {
	l := newRuntimeLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	apply := make(chan error, 1)
	go func() {
		apply <- l.runApply(context.Background(), []string{"b", "a"}, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- l.runStop(context.Background(), func() error { return nil }) }()
	waitLifecycleState(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.stopping })
	close(release)
	if err := <-apply; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLifecycleWaitingApplyDoesNotStartAfterStop(t *testing.T) {
	l := newRuntimeLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- l.runMutation(context.Background(), "in", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	applyCalled := atomic.Bool{}
	apply := make(chan error, 1)
	go func() {
		apply <- l.runApply(context.Background(), []string{"in"}, func() error { applyCalled.Store(true); return nil })
	}()
	gate := l.gate("in")
	waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 1 })
	stop := make(chan error, 1)
	go func() { stop <- l.runStop(context.Background(), func() error { return nil }) }()
	waitLifecycleState(t, func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.stopping })
	if err := <-apply; !errors.Is(err, errRuntimeStopping) {
		t.Fatalf("queued apply = %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-stop; err != nil {
		t.Fatal(err)
	}
	if applyCalled.Load() {
		t.Fatal("apply callback started after Stop began")
	}
}

func TestRuntimeLifecycleApplyWaitsForMutationAndUsesCommittedState(t *testing.T) {
	l := newRuntimeLifecycle()
	state := "old"
	entered, release := make(chan struct{}), make(chan struct{})
	mutation := make(chan error, 1)
	go func() {
		mutation <- l.runMutation(context.Background(), "wg", func() error { close(entered); <-release; state = "committed"; return nil })
	}()
	<-entered
	apply := make(chan error, 1)
	go func() {
		apply <- l.runApply(context.Background(), []string{"wg"}, func() error {
			if state != "committed" {
				return errors.New("stale policy snapshot")
			}
			state = "applied"
			return nil
		})
	}()
	gate := l.gate("wg")
	waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 1 })
	close(release)
	if err := <-mutation; err != nil {
		t.Fatal(err)
	}
	if err := <-apply; err != nil {
		t.Fatal(err)
	}
	if state != "applied" {
		t.Fatalf("final state = %q", state)
	}
}

func TestRuntimeLifecycleRepeatedMutationsCannotSeePartialConfig(t *testing.T) {
	l := newRuntimeLifecycle()
	state := map[string]int{"a": 1, "b": 1}
	entered, continueApply := make(chan struct{}), make(chan struct{})
	apply := make(chan error, 1)
	go func() {
		apply <- l.runApply(context.Background(), []string{"a", "b"}, func() error { state["a"] = 2; close(entered); <-continueApply; state["b"] = 2; return nil })
	}()
	<-entered
	mutations := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			mutations <- l.runMutation(context.Background(), "a", func() error {
				if state["a"] != 2 || state["b"] != 2 {
					return errors.New("mutation observed partial config")
				}
				return nil
			})
		}()
	}
	gate := l.gate("a")
	waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 2 })
	if err := l.runMutation(context.Background(), "independent", func() error { return nil }); err != nil {
		t.Fatalf("unrelated inbound blocked: %v", err)
	}
	close(continueApply)
	if err := <-apply; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-mutations; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeLifecycleTimeoutAndSortedApplyAvoidDeadlock(t *testing.T) {
	l := newRuntimeLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	owner := make(chan error, 1)
	go func() {
		owner <- l.runMutation(context.Background(), "x", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.runMutation(ctx, "x", func() error { return errors.New("unexpected execution") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock timeout = %v", err)
	}
	close(release)
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- l.runApply(context.Background(), []string{"a", "b"}, func() error { return nil }) }()
	go func() { results <- l.runApply(context.Background(), []string{"b", "a"}, func() error { return nil }) }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("multi-inbound lock inversion")
		}
	}
}

func TestLifecycleGateFIFOWithCanceledMiddleWaiter(t *testing.T) {
	gate := &lifecycleGate{}
	ownerRelease, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ownerRelease()

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	ctxC, cancelC := context.WithCancel(context.Background())
	defer cancelC()
	contexts := []context.Context{ctxA, ctxB, ctxC}
	cancels := []context.CancelFunc{cancelA, cancelB, cancelC}
	entered := make(chan int, len(contexts))
	finish := make([]chan struct{}, len(contexts))
	results := make(chan struct {
		index int
		err   error
	}, len(contexts))
	for i, ctx := range contexts {
		finish[i] = make(chan struct{})
		go func(index int, ctx context.Context) {
			release, acquireErr := gate.acquire(ctx)
			if acquireErr == nil {
				entered <- index
				<-finish[index]
				release()
			}
			results <- struct {
				index int
				err   error
			}{index: index, err: acquireErr}
		}(i, ctx)
		waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == i+1 })
	}

	cancels[1]()
	select {
	case result := <-results:
		if result.index != 1 || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled middle waiter result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled middle waiter did not leave the queue")
	}
	gate.mu.Lock()
	queued := len(gate.waiters)
	gate.mu.Unlock()
	if queued != 2 {
		t.Fatalf("queued waiter count after cancellation = %d, want 2", queued)
	}

	ownerRelease()
	for _, want := range []int{0, 2} {
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("FIFO waiter entered as %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d was not handed the lock", want)
		}
		close(finish[want])
		select {
		case result := <-results:
			if result.index != want || result.err != nil {
				t.Fatalf("waiter result = %+v, want index %d success", result, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d did not release", want)
		}
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.held || len(gate.waiters) != 0 {
		t.Fatalf("gate leaked ownership/waiters: held=%v waiters=%d", gate.held, len(gate.waiters))
	}
}

func TestLifecycleGateCancelRacingHandoffDoesNotLeakOrDoubleUnlock(t *testing.T) {
	for iteration := 0; iteration < 64; iteration++ {
		gate := &lifecycleGate{}
		ownerRelease, err := gate.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		canceled := make(chan error, 1)
		go func() {
			release, acquireErr := gate.acquire(ctx)
			if acquireErr == nil {
				release()
			}
			canceled <- acquireErr
		}()
		waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 1 })
		tailAcquired := make(chan func(), 1)
		go func() {
			release, acquireErr := gate.acquire(context.Background())
			if acquireErr != nil {
				tailAcquired <- nil
				return
			}
			tailAcquired <- release
		}()
		waitLifecycleState(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return len(gate.waiters) == 2 })

		// Make cancellation and owner release contend for the same gate mutex.
		gate.mu.Lock()
		cancel()
		released := make(chan struct{})
		go func() { ownerRelease(); close(released) }()
		gate.mu.Unlock()
		select {
		case err := <-canceled:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d canceled waiter error = %v", iteration, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d canceled waiter stranded", iteration)
		}
		select {
		case <-released:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d owner release stranded", iteration)
		}
		select {
		case release := <-tailAcquired:
			if release == nil {
				t.Fatalf("iteration %d tail waiter failed to acquire", iteration)
			}
			release()
			release()
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d tail waiter stranded", iteration)
		}
		gate.mu.Lock()
		leaked := gate.held || len(gate.waiters) != 0
		gate.mu.Unlock()
		if leaked {
			t.Fatalf("iteration %d left gate locked or queued", iteration)
		}
	}
}
