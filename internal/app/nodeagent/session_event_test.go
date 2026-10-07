package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestSendNativeSessionEvent(t *testing.T) {
	var received nativeSessionEvent

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}

			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("unexpected authorization: %q", got)
			}

			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}

			w.WriteHeader(http.StatusOK)
		},
	))
	defer server.Close()

	node := New(Config{DataDir: t.TempDir()})

	err := node.sendNativeSessionEvent(
		context.Background(),
		nativeRuntimeSessionCallback{
			URL:    server.URL,
			Token:  "test-token",
			NodeID: 7,
		},
		nativeSessionEvent{
			UserID:     42,
			Protocol:   "ov",
			InboundTag: "openvpn-main",
			SessionID:  "session-1",
			AssignedIP: "10.66.0.2",
			ClientIP:   "203.0.113.10",
			Event:      "start",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if received.NodeID != 7 {
		t.Fatalf("unexpected node id: %d", received.NodeID)
	}
	if received.UserID != 42 {
		t.Fatalf("unexpected user id: %d", received.UserID)
	}
	if received.Protocol != "ov" {
		t.Fatalf("unexpected protocol: %q", received.Protocol)
	}
	if received.Event != "start" {
		t.Fatalf("unexpected event: %q", received.Event)
	}
}

func TestDispatchNativeSessionEventsUsesIndependentTimeouts(t *testing.T) {
	previousClient := nativeSessionHTTPClient
	nativeSessionHTTPClient = &http.Client{Timeout: 40 * time.Millisecond}
	t.Cleanup(func() { nativeSessionHTTPClient = previousClient })

	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		requestNumber := requests
		mu.Unlock()
		if requestNumber == 1 {
			time.Sleep(80 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	done := make(chan error, 2)
	node := New(Config{DataDir: t.TempDir()})
	node.dispatchNativeSessionEvents(nativeRuntimeSessionCallback{
		URL: server.URL, NodeID: 1,
	}, []nativeSessionEvent{
		{UserID: 1, Protocol: "ov", SessionID: "one", Event: "seen"},
		{UserID: 1, Protocol: "ov", SessionID: "two", Event: "seen"},
	}, func(_ nativeSessionEvent, err error) { done <- err })

	first := <-done
	second := <-done
	if first != nil {
		t.Fatalf("offline callback should be queued: %v", first)
	}
	if second != nil {
		t.Fatalf("second event inherited the first timeout: %v", second)
	}
	queued, err := os.ReadDir(node.nativeSessionOutboxDir())
	if err != nil || len(queued) != 0 {
		t.Fatalf("recovered callback outbox not drained: %v %v", queued, err)
	}
}

func TestSendNativeSessionEventDeviceLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(
				w,
				"device limit reached",
				http.StatusConflict,
			)
		},
	))
	defer server.Close()

	node := New(Config{DataDir: t.TempDir()})

	err := node.sendNativeSessionEvent(
		context.Background(),
		nativeRuntimeSessionCallback{
			URL:    server.URL,
			Token:  "test-token",
			NodeID: 7,
		},
		nativeSessionEvent{
			UserID:    42,
			Protocol:  "ov",
			SessionID: "session-2",
			Event:     "start",
		},
	)

	if !errors.Is(err, errNativeSessionDeviceLimit) {
		t.Fatalf(
			"expected device limit error, got %v",
			err,
		)
	}
}

func TestSendNativeSessionEventAllowsMissingCallback(t *testing.T) {
	node := New(Config{DataDir: t.TempDir()})

	err := node.sendNativeSessionEvent(
		context.Background(),
		nativeRuntimeSessionCallback{},
		nativeSessionEvent{},
	)

	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeSessionOutboxSurvivesPanelOutageAndReplaysInOrder(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	node := New(Config{DataDir: t.TempDir()})
	callback := nativeRuntimeSessionCallback{URL: "http://" + address, Token: "test-token", NodeID: 7}
	start := nativeSessionEvent{UserID: 42, Protocol: "openvpn", InboundTag: "native", SessionID: "offline-session", Event: "start"}
	if err := node.sendNativeSessionEventOfflineSafe(context.Background(), callback, start); err != nil {
		t.Fatal(err)
	}
	queued, err := os.ReadDir(node.nativeSessionOutboxDir())
	if err != nil || len(queued) != 1 {
		t.Fatalf("offline start not durably queued: %v %v", queued, err)
	}
	dataDir := node.cfg.DataDir
	var received []nativeSessionEvent
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event nativeSessionEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			return
		}
		received = append(received, event)
		w.WriteHeader(http.StatusOK)
	}))
	defer panel.Close()
	callback.URL = panel.URL
	stop := start
	stop.Event, stop.SessionID = "stop", "offline-session"
	// The node agent process can restart while the panel is still down.
	node = New(Config{DataDir: dataDir})
	if err := node.sendNativeSessionEventOfflineSafe(context.Background(), callback, stop); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0].Event != "start" || received[1].Event != "stop" {
		t.Fatalf("queued events replayed out of order: %#v", received)
	}
	queued, err = os.ReadDir(node.nativeSessionOutboxDir())
	if err != nil || len(queued) != 0 {
		t.Fatalf("delivered events were not pruned: %v %v", queued, err)
	}
}
