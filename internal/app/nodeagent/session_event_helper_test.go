package nodeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRunNativeSessionEventHelperStartStop(t *testing.T) {
	var events []nativeSessionEvent

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var event nativeSessionEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}

			events = append(events, event)
			w.WriteHeader(http.StatusOK)
		},
	))
	defer server.Close()

	root := t.TempDir()
	configPath := filepath.Join(root, "session-helper.json")

	raw, err := json.Marshal(nativeSessionHelperConfig{
		Callback: nativeRuntimeSessionCallback{
			URL:    server.URL,
			Token:  "test-token",
			NodeID: 7,
		},
		InboundTag: "openvpn-main",
		Users: map[string]int64{
			"alice": 42,
		},
		StateDir: filepath.Join(root, "sessions"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("common_name", "alice")
	t.Setenv("ifconfig_pool_remote_ip", "10.66.0.10")
	t.Setenv("trusted_ip", "203.0.113.10")
	t.Setenv("trusted_port", "54321")

	if err := RunNativeSessionEventHelper(
		[]string{configPath, "start"},
	); err != nil {
		t.Fatal(err)
	}

	if err := RunNativeSessionEventHelper(
		[]string{configPath, "stop"},
	); err != nil {
		t.Fatal(err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Event != "start" {
		t.Fatalf("unexpected first event: %q", events[0].Event)
	}

	if events[1].Event != "stop" {
		t.Fatalf("unexpected second event: %q", events[1].Event)
	}

	if events[0].SessionID == "" {
		t.Fatal("empty session id")
	}

	if events[0].SessionID != events[1].SessionID {
		t.Fatalf(
			"session id changed: %q != %q",
			events[0].SessionID,
			events[1].SessionID,
		)
	}

	if events[0].AssignedIP != "10.66.0.10" {
		t.Fatalf(
			"unexpected assigned ip: %q",
			events[0].AssignedIP,
		)
	}

	if events[0].ClientIP != "203.0.113.10" {
		t.Fatalf(
			"unexpected client ip: %q",
			events[0].ClientIP,
		)
	}
}

func TestRunNativeSessionEventHelperL2TPEnvironment(t *testing.T) {
	var events []nativeSessionEvent

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var event nativeSessionEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
			w.WriteHeader(http.StatusOK)
		},
	))
	defer server.Close()

	root := t.TempDir()
	configPath := filepath.Join(root, "session-helper.json")
	raw, err := json.Marshal(nativeSessionHelperConfig{
		Callback: nativeRuntimeSessionCallback{
			URL:    server.URL,
			Token:  "test-token",
			NodeID: 7,
		},
		InboundTag: "l2tp-main",
		Protocol:   "l2tp",
		Users: map[string]int64{
			"alice-vpn": 42,
		},
		StateDir: filepath.Join(root, "sessions"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PEERNAME", "alice-vpn")
	t.Setenv("IPREMOTE", "10.67.0.10")
	t.Setenv("CALLING_NUMBER", "203.0.113.10")

	if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Protocol != "l2tp" {
		t.Fatalf("protocol = %q, want l2tp", event.Protocol)
	}
	if event.InboundTag != "l2tp-main" || event.UserID != 42 {
		t.Fatalf("unexpected event identity: %#v", event)
	}
	if event.AssignedIP != "10.67.0.10" || event.ClientIP != "203.0.113.10" {
		t.Fatalf("unexpected event addresses: %#v", event)
	}
}
