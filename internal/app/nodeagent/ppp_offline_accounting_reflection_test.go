package nodeagent

import "testing"

func TestOfflineQuotaPendingReflectionAndRetainedNewerTraffic(t *testing.T) {
	for _, protocol := range []string{"openvpn", "l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			deliveries := []localUsageDelivery{
				{BatchID: "root-old", Rows: []localUsageDeliveryRow{{Protocol: protocol, ChildID: "acked-old", UserID: 42, InboundTag: "tag", Raw: 80}}},
				{BatchID: "root-pending", Rows: []localUsageDeliveryRow{{Protocol: protocol, ChildID: "child-pending", UserID: 42, InboundTag: "tag", Raw: 100}}},
			}
			if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
				t.Fatal(err)
			}
			key := offlineAccountingTotalKey(42, "tag")
			baseline := map[string]uint64{key: 350, offlineAccountingSentKey(key): 50}
			// Unsent 300 includes pending 100 + newer 200; add old unreflected 80.
			raw, err := s.offlinePolicyRaw(baseline, protocol, 42, "tag", "child-pending", "", 100)
			if err != nil || raw != 380 {
				t.Fatalf("before reflection raw=%d err=%v", raw, err)
			}
			// Panel has reflected both deliveries but ACK of current child was lost.
			raw, err = s.offlinePolicyRaw(baseline, protocol, 42, "tag", "child-pending", "root-pending", 100)
			if err != nil || raw != 200 {
				t.Fatalf("pending reflected raw=%d err=%v", raw, err)
			}
			// Repeated checks after safe receipt pruning must retain the pending identity.
			raw, err = s.offlinePolicyRaw(baseline, protocol, 42, "tag", "child-pending", "root-pending", 100)
			if err != nil || raw != 200 {
				t.Fatalf("repeated reflection raw=%d err=%v", raw, err)
			}
			// ACK advances only the sent snapshot; the same newer bytes remain.
			baseline[offlineAccountingSentKey(key)] = 150
			raw, err = s.offlinePolicyRaw(baseline, protocol, 42, "tag", "", "root-pending", 0)
			if err != nil || raw != 200 {
				t.Fatalf("after ACK raw=%d err=%v", raw, err)
			}
			policy := nativeSessionUserPolicy{Status: "active", DataLimit: 1000, UsedTraffic: 400, UsageCoefficient: 2, InboundCoefficient: 1.5}
			if nativeSessionEffectiveLiveUsage(policy, raw) != 600 {
				t.Fatal("coefficient was not applied exactly once")
			}
		})
	}
}

func TestOfflineQuotaReflectedPendingUnderflowFails(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	deliveries := []localUsageDelivery{{BatchID: "root", Rows: []localUsageDeliveryRow{{Protocol: "pptp", ChildID: "pending", UserID: 42, InboundTag: "tag", Raw: 100}}}}
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	if _, err := s.offlinePolicyRaw(map[string]uint64{offlineAccountingTotalKey(42, "tag"): 50}, "pptp", 42, "tag", "pending", "root", 100); err == nil {
		t.Fatal("underflow accepted")
	}
}

func TestNativePolicyCopiesReflectedRootMarker(t *testing.T) {
	policies := buildNativeSessionUserPolicies([]openVPNRuntimeUser{{UserID: 42, VPNUsername: "alice", ReflectedUsageBatchID: "root-reflected"}})
	if policies["alice"].ReflectedUsageBatchID != "root-reflected" {
		t.Fatal("reflection marker lost")
	}
}

func TestOfflineQuotaLegacyPendingFallbackAndReflection(t *testing.T) {
	for _, protocol := range []string{"openvpn", "l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			deliveries := []localUsageDelivery{
				{BatchID: "old-root", Rows: []localUsageDeliveryRow{{Protocol: protocol, ChildID: "old-child", UserID: 42, InboundTag: "tag", Raw: 80}}},
				{BatchID: "pending-root", Rows: []localUsageDeliveryRow{{Protocol: protocol, ChildID: "pending-child", UserID: 42, InboundTag: "tag", Raw: 500}}},
			}
			if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
				t.Fatal(err)
			}
			// Legacy state contains only native baselines and immutable pending samples.
			legacy := map[string]uint64{"legacy-native-counter": 500}
			raw, err := s.offlinePolicyRaw(legacy, protocol, 42, "tag", "pending-child", "", 500)
			if err != nil || raw != 580 {
				t.Fatalf("legacy pending+credit: raw=%d err=%v", raw, err)
			}
			raw, err = s.offlinePolicyRaw(legacy, protocol, 42, "tag", "pending-child", "pending-root", 500)
			if err != nil || raw != 0 {
				t.Fatalf("reflected legacy pending: raw=%d err=%v", raw, err)
			}
			// An existing retained-total key wins, including a legitimate zero total.
			explicit := map[string]uint64{offlineAccountingTotalKey(42, "tag"): 0}
			if _, err := s.offlinePolicyRaw(explicit, protocol, 42, "tag", "pending-child", "pending-root", 500); err == nil {
				t.Fatal("fallback hid invalid retained-total underflow")
			}
		})
	}
}
