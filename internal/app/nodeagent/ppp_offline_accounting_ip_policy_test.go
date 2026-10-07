package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeOfflineIPLimitDistinctRealAddresses(t *testing.T) {
	observations := []nativeOfflineIPObservation{{"a", 42, "198.51.100.1"}, {"b", 42, "198.51.100.1"}, {"c", 42, "198.51.100.2"}, {"d", 99, "198.51.100.3"}}
	denied := nativeOfflineIPLimitDenied(observations, map[int64]int64{42: 1, 99: 1})
	if !denied["c"] || denied["a"] || denied["b"] || denied["d"] {
		t.Fatalf("distinct-IP selection: %v", denied)
	}
	observations = append(observations, nativeOfflineIPObservation{"missing", 42, "caller-name"})
	if denied := nativeOfflineIPLimitDenied(observations, map[int64]int64{42: 1}); len(denied) != 0 {
		t.Fatalf("enforced unreliable real-IP metadata: %v", denied)
	}
}

func TestNativeOfflineDeviceLimitKeepsStablePPPIdentity(t *testing.T) {
	observations := []nativeOfflineIPObservation{
		{SessionID: "session-b", UserID: 42, RealIP: "198.51.100.2"},
		{SessionID: "session-a", UserID: 42, RealIP: "198.51.100.1"},
		{SessionID: "other-user", UserID: 99, RealIP: "198.51.100.3"},
	}
	denied := nativeOfflineDeviceLimitDenied(observations, map[int64]int64{42: 1, 99: 1})
	if len(denied) != 1 || !denied["session-b"] {
		t.Fatalf("denied sessions = %v, want only session-b", denied)
	}
	if denied := nativeOfflineDeviceLimitDenied(observations, map[int64]int64{42: 2}); len(denied) != 0 {
		t.Fatalf("denied sessions below limit = %v", denied)
	}
}

func TestOpenVPNOfflineIPLimitDoesNotUseAssignedIPOrDeviceLimit(t *testing.T) {
	cfg := nativeSessionHelperConfig{Users: map[string]int64{"alice": 42}, Policies: map[string]nativeSessionUserPolicy{"alice": {IPLimit: 1, DeviceLimit: 99}}}
	clients := []openVPNStatusClient{
		{Username: "alice", ClientID: "1", RealAddress: "198.51.100.1:100", VirtualAddress: "10.0.0.1"},
		{Username: "alice", ClientID: "2", RealAddress: "198.51.100.1:200", VirtualAddress: "10.0.0.2"},
		{Username: "alice", ClientID: "3", RealAddress: "198.51.100.2:100", VirtualAddress: "10.0.0.1"},
	}
	denied := openVPNOfflineIPDenied(cfg, clients)
	if !denied["3"] || len(denied) != 1 {
		t.Fatalf("used tunnel address or session/device count: %v", denied)
	}
	cfg.Policies["alice"] = nativeSessionUserPolicy{DeviceLimit: 1, IPLimit: 0}
	if denied := openVPNOfflineIPDenied(cfg, clients); len(denied) != 0 {
		t.Fatal("device limit misinterpreted as IP limit")
	}
}

func TestNativePolicyCopiesSeparateDeviceAndIPLimits(t *testing.T) {
	policies := buildNativeSessionUserPolicies([]openVPNRuntimeUser{{VPNUsername: "alice", UserID: 42, DeviceLimit: 7, IPLimit: 2}})
	if policies["alice"].DeviceLimit != 7 || policies["alice"].IPLimit != 2 {
		t.Fatalf("limits conflated: %+v", policies["alice"])
	}
}

func TestNativePolicyCopiesAllOpenVPNEnforcementFields(t *testing.T) {
	limit := int64(50 * 1024 * 1024)
	expire := int64(2_000_000_000)
	policies := buildNativeSessionUserPolicies([]openVPNRuntimeUser{{
		VPNUsername: "alice", Status: "active", UsedTraffic: 11,
		DataLimit: &limit, Expire: &expire, DeviceLimit: 3, IPLimit: 2,
		UploadSpeedLimit: 1234, DownloadSpeedLimit: 5678,
		UsageCoefficient: 1.5, InboundCoefficient: 2,
	}})
	policy := policies["alice"]
	if policy.DataLimit != limit || policy.Expire != expire || policy.DeviceLimit != 3 || policy.IPLimit != 2 ||
		policy.UploadSpeedLimit != 1234 || policy.DownloadSpeedLimit != 5678 ||
		policy.UsageCoefficient != 1.5 || policy.InboundCoefficient != 2 {
		t.Fatalf("OpenVPN enforcement fields were not copied: %+v", policy)
	}
}

func TestPPPOfflineAdmissionLimitUsesLiveSessionsAndOuterIP(t *testing.T) {
	root := t.TempDir()
	pptpRoot := filepath.Join(root, "pptp", "pptp-runtime")
	l2tpRoot := filepath.Join(root, "l2tp", "l2tp-runtime")
	for _, runtimeRoot := range []string{pptpRoot, l2tpRoot} {
		if err := os.MkdirAll(filepath.Join(runtimeRoot, "ppp-accounting", "active"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		root    string
		session pppOfflineSession
	}{
		{pptpRoot, pppOfflineSession{ID: "pptp-session", UserID: 7, Interface: "ppp0", PeerIP: "10.0.0.2", ClientIP: "198.51.100.10", Identity: "identity-ppp0", Process: "boot:101:1"}},
		{l2tpRoot, pppOfflineSession{ID: "l2tp-session", UserID: 7, Interface: "ppp1", PeerIP: "10.0.0.3", ClientIP: "198.51.100.11", Identity: "identity-ppp1", Process: "boot:102:1"}},
		{l2tpRoot, pppOfflineSession{ID: "stale-session", UserID: 7, Interface: "ppp2", PeerIP: "10.0.0.4", ClientIP: "198.51.100.12", Identity: "identity-ppp2", Process: "boot:104:1"}},
	} {
		raw, err := json.Marshal(fixture.session)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pppOfflineSessionActivePath(fixture.root, fixture.session.Interface, fixture.session.Process), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldIdentity, oldProcess := pppOfflineReadIdentity, pppOfflineReadProcess
	pppOfflineReadIdentity = func(iface string) (string, error) {
		switch iface {
		case "ppp0":
			return "identity-ppp0", nil
		case "ppp1":
			return "identity-ppp1", nil
		default:
			return "interface-gone", nil
		}
	}
	pppOfflineReadProcess = func(pid string) (string, error) { return "boot:" + pid + ":1", nil }
	t.Cleanup(func() { pppOfflineReadIdentity, pppOfflineReadProcess = oldIdentity, oldProcess })
	if err := pppOfflineWriteAdmissionReservation(root, 7, "test", "boot:103:1", "198.51.100.11"); err != nil {
		t.Fatal(err)
	}
	if denied, reason, err := pppOfflineAdmissionLimit(root, 7, "198.51.100.20", "", nativeSessionUserPolicy{DeviceLimit: 3}); err != nil || !denied || reason != "device limit reached" {
		t.Fatalf("durable pending admission was not counted: %v, %q, %v", denied, reason, err)
	}
	if denied, reason, err := pppOfflineAdmissionLimit(root, 7, "198.51.100.20", "boot:103:1", nativeSessionUserPolicy{DeviceLimit: 3}); err != nil || denied {
		t.Fatalf("current process reservation was double-counted: %v, %q, %v", denied, reason, err)
	}
	if denied, reason, err := pppOfflineAdmissionLimit(root, 7, "198.51.100.20", "", nativeSessionUserPolicy{DeviceLimit: 2}); err != nil || !denied || reason != "device limit reached" {
		t.Fatalf("DeviceLimit admission = %v, %q, %v", denied, reason, err)
	}
	if denied, reason, err := pppOfflineAdmissionLimit(root, 7, "198.51.100.20", "", nativeSessionUserPolicy{IPLimit: 1}); err != nil || !denied || reason != "IP limit reached" {
		t.Fatalf("IPLimit admission = %v, %q, %v", denied, reason, err)
	}
	if denied, reason, err := pppOfflineAdmissionLimit(root, 7, "198.51.100.10", "", nativeSessionUserPolicy{IPLimit: 2}); err != nil || denied {
		t.Fatalf("known source IP should remain admissible at the configured limit: %v, %q, %v", denied, reason, err)
	}
}
