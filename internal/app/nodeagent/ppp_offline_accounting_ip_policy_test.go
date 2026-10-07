package nodeagent

import "testing"

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
