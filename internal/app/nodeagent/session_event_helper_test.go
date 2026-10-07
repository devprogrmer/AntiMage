package nodeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenVPNAdmissionRejectsDurableOfflineQuota(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "openvpn", "native")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "session-helper.json")
	if err := offlineDurableJSON(configPath, nativeSessionHelperConfig{
		Protocol: "openvpn", InboundTag: "native", Users: map[string]int64{"alice": 42},
		Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 100, UsageCoefficient: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("common_name", "alice")
	for _, used := range []uint64{49, 50, 51} {
		s := New(Config{DataDir: dir})
		key := offlineAccountingTotalKey(42, "native")
		s.openVPNUsageBaseline = map[string]uint64{key: used}
		// A lost panel ACK leaves the immutable batch pending while its raw
		// snapshot is already retained in the durable baseline. Admission must
		// count the raw bytes once: 49 is still below the 100 effective-byte
		// quota at coefficient 2, while 50 and 51 must remain blocked.
		s.openVPNUsagePending = &openVPNUsagePendingBatch{
			BatchID:      "openvpn-native-pending",
			Samples:      []openVPNUsageSample{{UserID: 42, InboundTag: "native", Value: used}},
			NextBaseline: map[string]uint64{key: used},
		}
		if err := s.persistOpenVPNUsageStateLocked(); err != nil {
			t.Fatal(err)
		}
		err := RunNativeSessionEventHelper([]string{configPath, "start"})
		if used < 50 && err != nil {
			t.Fatalf("below quota: %v", err)
		}
		if used >= 50 && (err == nil || !strings.Contains(err.Error(), "data limit reached")) {
			t.Fatalf("used=%d admission=%v", used, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "openvpn", "usage-state.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err == nil {
		t.Fatal("corrupt accounting admitted client")
	}
}

func TestPPPPreUpAdmissionRejectsDurableOfflineQuota(t *testing.T) {
	dir := t.TempDir()
	runtimeRoot := filepath.Join(dir, "pptp", "native")
	if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(runtimeRoot, "session-helper.json")
	if err := offlineDurableJSON(configPath, nativeSessionHelperConfig{
		Protocol: "pptp", InboundTag: "native", Users: map[string]int64{"alice": 42},
		Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 100}},
	}); err != nil {
		t.Fatal(err)
	}
	usageStatePath := filepath.Join(dir, "pptp", "usage-state.json")
	if err := offlineDurableJSON(usageStatePath, pptpUsageDiskState{
		Baseline: map[string]uint64{offlineAccountingTotalKey(42, "native"): 99},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("common_name", "alice")
	t.Setenv("PPPD_PID", "123")
	t.Setenv("IPPARAM", "198.51.100.10")
	previousProcess, previousSignal, previousProtocol := pppOfflineReadProcess, nativePPPProcessSignal, pppOfflineDetectProtocol
	pppOfflineDetectProtocol = func(string) (string, error) { return "pptp", nil }
	pppOfflineReadProcess = func(pid string) (string, error) {
		if pid != "123" {
			t.Fatalf("unexpected pppd PID: %s", pid)
		}
		return "fixture-boot:123:456", nil
	}
	signaled := ""
	nativePPPProcessSignal = func(identity string) error { signaled = identity; return nil }
	t.Cleanup(func() {
		pppOfflineReadProcess, nativePPPProcessSignal = previousProcess, previousSignal
		pppOfflineDetectProtocol = previousProtocol
	})
	if err := RunNativeSessionEventHelper([]string{configPath, "pre-up"}); err != nil {
		t.Fatalf("below-quota PPP admission rejected: %v", err)
	}
	if signaled != "" {
		t.Fatalf("below-quota PPP admission signaled pppd: %q", signaled)
	}
	if err := offlineDurableJSON(usageStatePath, pptpUsageDiskState{
		Baseline: map[string]uint64{offlineAccountingTotalKey(42, "native"): 100},
	}); err != nil {
		t.Fatal(err)
	}

	err := RunNativeSessionEventHelper([]string{configPath, "pre-up"})
	if err == nil || !strings.Contains(err.Error(), "data limit reached") {
		t.Fatalf("expected quota denial before PPP interface activation, got %v", err)
	}
	if signaled != "fixture-boot:123:456" {
		t.Fatalf("quota denial did not terminate the exact PPPD identity: %q", signaled)
	}
	var denial struct {
		Protocol string `json:"protocol"`
		UserID   int64  `json:"user_id"`
		PeerIP   string `json:"peer_ip"`
		Reason   string `json:"reason"`
		Process  string `json:"process"`
	}
	raw, err := os.ReadFile(filepath.Join(runtimeRoot, "ppp-accounting", "admission-denials", "42.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &denial); err != nil {
		t.Fatal(err)
	}
	if denial.Protocol != "pptp" || denial.UserID != 42 || denial.PeerIP != "" || denial.Reason != "data limit reached" || denial.Process != signaled {
		t.Fatalf("unexpected durable PPP admission denial: %+v", denial)
	}

	// Some pppd builds still launch ip-up while the pre-up denial is signalling
	// the process. The session boundary must independently reject that start.
	if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err == nil || !strings.Contains(err.Error(), "data limit reached") {
		t.Fatalf("quota-exhausted PPP start was not rejected: %v", err)
	}
	if signaled != "fixture-boot:123:456" {
		t.Fatalf("quota-exhausted session start did not signal its PPPD identity: %q", signaled)
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("quota-exhausted PPP start left session state behind: stat err=%v", err)
	}
}

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
		Policies:   map[string]nativeSessionUserPolicy{"alice": {Status: "active"}},
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
	testRunNativeSessionEventHelperPPPEnvironment(t, "l2tp", "l2tp-main")
}

func TestRunNativeSessionEventHelperPPTPEnvironment(t *testing.T) {
	testRunNativeSessionEventHelperPPPEnvironment(t, "pptp", "pptp-main")
}

func testRunNativeSessionEventHelperPPPEnvironment(
	t *testing.T,
	protocol,
	inboundTag string,
) {
	t.Helper()
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
		InboundTag: inboundTag,
		Protocol:   protocol,
		Policies:   map[string]nativeSessionUserPolicy{"alice-vpn": {Status: "active"}},
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
	t.Setenv("trusted_ip", "")
	t.Setenv("trusted_ip6", "")
	t.Setenv("IPPARAM", "antimage-"+protocol)
	t.Setenv("REMOTENUMBER", "203.0.113.10")
	t.Setenv("IP_REAL", "")
	t.Setenv("CALLING_NUMBER", "+1-202-555-0199")
	t.Setenv("IFNAME", "ppp0")
	t.Setenv("PPPD_PID", "123")
	previousIdentity, previousProcess := pppOfflineReadIdentity, pppOfflineReadProcess
	pppOfflineReadIdentity = func(string) (string, error) { return "fixture-boot:1", nil }
	pppOfflineReadProcess = func(string) (string, error) { return "fixture-boot:123:456", nil }
	t.Cleanup(func() { pppOfflineReadIdentity = previousIdentity; pppOfflineReadProcess = previousProcess })

	if err := RunNativeSessionEventHelper([]string{configPath, "start", "203.0.113.11"}); err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event := events[0]
	if event.Protocol != protocol {
		t.Fatalf("protocol = %q, want %s", event.Protocol, protocol)
	}
	if event.InboundTag != inboundTag || event.UserID != 42 {
		t.Fatalf("unexpected event identity: %#v", event)
	}
	if event.AssignedIP != "10.67.0.10" || event.ClientIP != "203.0.113.11" {
		t.Fatalf("unexpected event addresses: %#v", event)
	}
}

func TestFirstNonEmptyIPEnvRejectsNonIPCallingNumber(t *testing.T) {
	t.Setenv("trusted_ip", "not-an-ip")
	t.Setenv("trusted_ip6", "")
	t.Setenv("IPPARAM", "antimage-l2tp")
	t.Setenv("REMOTENUMBER", "203.0.113.10")
	t.Setenv("CALLING_NUMBER", "+1-202-555-0199")
	t.Setenv("IP_REAL", "2001:db8::1")

	if got := firstNonEmptyIPEnv("trusted_ip", "trusted_ip6", "IPPARAM", "REMOTENUMBER", "IP_REAL"); got != "203.0.113.10" {
		t.Fatalf("firstNonEmptyIPEnv() = %q, want valid REMOTENUMBER before IP_REAL", got)
	}
	t.Setenv("REMOTENUMBER", "")
	if got := firstNonEmptyIPEnv("trusted_ip", "trusted_ip6", "IPPARAM", "REMOTENUMBER", "IP_REAL"); got != "2001:db8::1" {
		t.Fatalf("firstNonEmptyIPEnv() = %q, want valid IP_REAL after invalid values", got)
	}
	t.Setenv("IP_REAL", "")
	if got := firstNonEmptyIPEnv("trusted_ip", "trusted_ip6", "IPPARAM", "REMOTENUMBER", "IP_REAL"); got != "" {
		t.Fatalf("firstNonEmptyIPEnv() = %q, want empty when no IP address is available", got)
	}
}

func TestNativeSessionDeviceIDOpenVPN(t *testing.T) {
	first := nativeSessionDeviceID("ov", "ov-session-a")
	same := nativeSessionDeviceID("openvpn", "ov-session-a")
	other := nativeSessionDeviceID("ov", "ov-session-b")

	if first == "" || first != same || first == other {
		t.Fatalf("invalid OpenVPN device IDs: first=%q same=%q other=%q", first, same, other)
	}
}
