package nodeagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepeatedNativeSessionStopsAreCoalesced(t *testing.T) {
	var calls atomic.Int64
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer panel.Close()
	s := New(Config{DataDir: t.TempDir()})
	cb := nativeRuntimeSessionCallback{URL: panel.URL, NodeID: 1, Token: "one"}
	event := nativeSessionEvent{UserID: 7, Protocol: "wg", InboundTag: "wg", SessionID: "peer", Event: "stop"}
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.sendNativeSessionEvent(context.Background(), cb, event); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("one acknowledged stop generated %d HTTP requests", got)
	}
	// A live observation resets the stop receipt; reconnects must close again.
	event.Event = "seen"
	if err := s.sendNativeSessionEvent(context.Background(), cb, event); err != nil {
		t.Fatal(err)
	}
	event.Event = "stop"
	if err := s.sendNativeSessionEvent(context.Background(), cb, event); err != nil {
		t.Fatal(err)
	}
	cb.Token = "rotated"
	if err := s.sendNativeSessionEvent(context.Background(), cb, event); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("transitions/credential rotation lost: %d", got)
	}
}

func TestFailedNativeSessionStopIsRetried(t *testing.T) {
	var calls atomic.Int64
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer panel.Close()
	s := New(Config{DataDir: t.TempDir()})
	cb := nativeRuntimeSessionCallback{URL: panel.URL, NodeID: 1}
	event := nativeSessionEvent{UserID: 7, Protocol: "wg", SessionID: "peer", Event: "stop"}
	if err := s.sendNativeSessionEvent(context.Background(), cb, event); err == nil {
		t.Fatal("expected callback failure")
	}
	for range 2 {
		if err := s.sendNativeSessionEvent(context.Background(), cb, event); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("retry/dedup requests=%d", got)
	}
}

func TestNativeSessionStopReceiptRefreshAndIsolation(t *testing.T) {
	var calls atomic.Int64
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer panel.Close()
	s := New(Config{DataDir: t.TempDir()})
	cb := nativeRuntimeSessionCallback{URL: panel.URL, NodeID: 1}
	event := nativeSessionEvent{UserID: 7, Protocol: "wg", SessionID: "peer", Event: "stop"}
	send := func(callback nativeRuntimeSessionCallback, e nativeSessionEvent) {
		t.Helper()
		if err := s.sendNativeSessionEvent(context.Background(), callback, e); err != nil {
			t.Fatal(err)
		}
	}
	send(cb, event)
	receipt := s.nativeSessionReceiptFor(cb, func() nativeSessionEvent { e := event; e.NodeID = cb.NodeID; return e }())
	receipt.lastSuccess = time.Now().Add(-nativeSessionStopRefresh - time.Second)
	send(cb, event)
	changed := event
	changed.ClientIP = "203.0.113.2"
	send(cb, changed)
	changed.UserID++
	send(cb, changed)
	cb.NodeID++
	send(cb, changed)
	changed.Event = "seen"
	send(cb, changed)
	send(cb, changed) // Heartbeats keep their original cadence.
	if got := calls.Load(); got != 7 {
		t.Fatalf("refresh/isolation/heartbeats lost: %d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.sendNativeSessionEvent(ctx, cb, changed); err == nil {
		t.Fatal("canceled callback succeeded")
	}
	if got := calls.Load(); got != 7 {
		t.Fatal("canceled callback sent a request")
	}
}

func TestNativeSessionReceiptCapacityFallsBackWithoutDroppingEvents(t *testing.T) {
	s := &Server{nativeSessionReceipts: make(map[nativeSessionReceiptKey]*nativeSessionReceipt)}
	cb := nativeRuntimeSessionCallback{URL: "http://panel", NodeID: 1}
	event := nativeSessionEvent{UserID: 1, Protocol: "wg", Event: "stop"}
	for i := 0; i < maxNativeSessionReceipts; i++ {
		event.UserID = int64(i + 1)
		if s.nativeSessionReceiptFor(cb, event) == nil {
			t.Fatal("premature cache limit")
		}
	}
	event.UserID++
	if s.nativeSessionReceiptFor(cb, event) != nil {
		t.Fatal("receipt cache grew beyond limit")
	}
	event.UserID = 1
	if s.nativeSessionReceiptFor(cb, event) == nil {
		t.Fatal("existing receipt unavailable at capacity")
	}
}
