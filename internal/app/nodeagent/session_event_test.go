package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
