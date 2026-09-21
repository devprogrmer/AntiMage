package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForWireGuardSessionTest(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for asynchronous session callback")
}

func TestWireGuardEndpointHost(t *testing.T) {
	for raw, want := range map[string]string{
		"198.51.100.4:20000":   "198.51.100.4",
		"[2001:db8::10]:51820": "2001:db8::10",
		"(none)":               "",
		"":                     "",
	} {
		if got := wireGuardEndpointHost(raw); got != want {
			t.Fatalf("endpoint %q = %q, want %q", raw, got, want)
		}
	}
}

func TestWireGuardSessionDeviceLimitRemovesPeerAfterAccountingSnapshot(t *testing.T) {
	var mu sync.Mutex
	var events []nativeSessionEvent

	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	server.wireGuardUsageBaseline[key] = 100

	httpServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var event nativeSessionEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Error(err)
				return
			}

			mu.Lock()
			events = append(events, event)
			mu.Unlock()

			if event.Event == "seen" {
				http.Error(w, "device limit reached", http.StatusConflict)
				return
			}

			w.WriteHeader(http.StatusOK)
		},
	))
	defer httpServer.Close()

	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun

	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	snapshotObservedAtRemoval := false
	var runtimeMu sync.Mutex

	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		runtimeMu.Lock()
		defer runtimeMu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))

		carry, ok := server.wireGuardUsageCarry[key]
		if ok &&
			carry.Value == 50 &&
			carry.NextBaseline == 150 {
			snapshotObservedAtRemoval = true
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	accountingEnabled := true
	now := time.Now().UTC()

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:        "wg-main",
		InterfaceName:     "wg-test0",
		AccountingEnabled: &accountingEnabled,
		Peers: map[string]int64{
			"peer-a": 42,
		},
		PeerAddresses: map[string]string{
			"peer-a": "10.69.0.42",
		},
		Callback: nativeRuntimeSessionCallback{
			URL:    httpServer.URL,
			NodeID: 9,
		},
	}

	err := server.reconcileWireGuardSessions(
		context.Background(),
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:       "peer-a",
			Endpoint:        "198.51.100.4:20000",
			LatestHandshake: now.Unix(),
			ReceivedBytes:   100,
			SentBytes:       50,
		}},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForWireGuardSessionTest(t, func() bool {
		mu.Lock()
		eventCount := len(events)
		mu.Unlock()

		runtimeMu.Lock()
		callCount := len(calls)
		runtimeMu.Unlock()

		suppressionKey := wireGuardDynamicSuppressionKey("wg-main", "peer-a")
		server.mu.Lock()
		_, suppressed := server.wireGuardDynamicSuppressedPeers[suppressionKey]
		server.mu.Unlock()

		return eventCount == 1 && callCount == 1 && suppressed
	})

	mu.Lock()
	if len(events) != 1 {
		mu.Unlock()
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	mu.Unlock()

	if event.Protocol != "wg" {
		t.Fatalf("protocol = %q", event.Protocol)
	}
	if event.AssignedIP != "10.69.0.42" {
		t.Fatalf("assigned ip = %q", event.AssignedIP)
	}
	if event.ClientIP != "198.51.100.4" {
		t.Fatalf("client ip = %q", event.ClientIP)
	}

	runtimeMu.Lock()
	if len(calls) != 1 {
		runtimeMu.Unlock()
		t.Fatalf(
			"wireguard runtime calls = %v, want exactly one",
			calls,
		)
	}

	want := "wg set wg-test0 peer peer-a remove"
	if calls[0] != want {
		got := calls[0]
		runtimeMu.Unlock()
		t.Fatalf(
			"wireguard runtime call = %q, want %q",
			got,
			want,
		)
	}

	if !snapshotObservedAtRemoval {
		runtimeMu.Unlock()
		t.Fatal("peer removal happened before persisted accounting snapshot")
	}
	runtimeMu.Unlock()

	server.wireGuardUsageMu.Lock()
	carry, ok := server.wireGuardUsageCarry[key]
	server.wireGuardUsageMu.Unlock()
	if !ok {
		t.Fatal("accounting snapshot carry missing")
	}
	if carry.Value != 50 {
		t.Fatalf("snapshot carry = %d, want 50", carry.Value)
	}
	if carry.NextBaseline != 150 {
		t.Fatalf(
			"snapshot next baseline = %d, want 150",
			carry.NextBaseline,
		)
	}

	suppressionKey := wireGuardDynamicSuppressionKey("wg-main", "peer-a")
	server.mu.Lock()
	if _, ok := server.wireGuardDynamicSuppressedPeers[suppressionKey]; !ok {
		server.mu.Unlock()
		t.Fatal("device-limit disconnect did not suppress peer from unchanged runtime apply")
	}
	server.mu.Unlock()
}
func TestWireGuardSessionsStopStaleBeforeSeen(t *testing.T) {
	var mu sync.Mutex
	var order []string

	httpServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var event nativeSessionEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			order = append(order, event.Event+":"+event.SessionID)
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		},
	))
	defer httpServer.Close()

	server := New(Config{DataDir: t.TempDir()})
	now := time.Now().UTC()

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag: "wg-main",
		Peers: map[string]int64{
			"peer-stale": 1,
			"peer-live":  2,
		},
		PeerAddresses: map[string]string{
			"peer-stale": "10.69.0.2",
			"peer-live":  "10.69.0.3",
		},
		Callback: nativeRuntimeSessionCallback{
			URL:    httpServer.URL,
			NodeID: 9,
		},
	}

	err := server.reconcileWireGuardSessions(
		context.Background(),
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{
			{
				PublicKey:       "peer-live",
				Endpoint:        "198.51.100.10:10000",
				LatestHandshake: now.Unix(),
			},
			{
				PublicKey:       "peer-stale",
				Endpoint:        "198.51.100.11:10001",
				LatestHandshake: now.Add(-2 * time.Minute).Unix(),
			},
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForWireGuardSessionTest(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 2
	})

	mu.Lock()
	defer mu.Unlock()

	if len(order) != 2 {
		t.Fatalf("events = %#v, want stop then seen", order)
	}
	if !strings.HasPrefix(order[0], "stop:") {
		t.Fatalf("first event = %q, want stop", order[0])
	}
	if !strings.HasPrefix(order[1], "seen:") {
		t.Fatalf("second event = %q, want seen", order[1])
	}
}
func TestWireGuardSessionDeviceLimitSnapshotFailureBlocksRemoval(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "device limit reached", http.StatusConflict)
		},
	))
	defer httpServer.Close()

	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun

	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	accountingEnabled := true
	now := time.Now().UTC()

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:        "wg-main",
		InterfaceName:     "wg-test0",
		AccountingEnabled: &accountingEnabled,
		Peers: map[string]int64{
			"peer-a": 42,
		},
		Callback: nativeRuntimeSessionCallback{
			URL:    httpServer.URL,
			NodeID: 9,
		},
	}

	err := server.reconcileWireGuardSessions(
		context.Background(),
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:       "peer-a",
			Endpoint:        "198.51.100.4:20000",
			LatestHandshake: now.Unix(),
			ReceivedBytes:   ^uint64(0),
			SentBytes:       1,
		}},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Fatalf(
			"peer removed despite failed accounting snapshot: %v",
			calls,
		)
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	if _, exists := server.wireGuardUsageCarry[key]; exists {
		t.Fatal("invalid accounting snapshot was persisted")
	}
}
