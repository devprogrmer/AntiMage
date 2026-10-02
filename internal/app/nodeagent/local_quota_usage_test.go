package nodeagent

import (
	"testing"
	"time"
)

func TestLocalQuotaCombinesProtocolsAndDoesNotMultiplyReflectedUsage(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	policy := nativeSessionUserPolicy{Status: "active", DataLimit: 50, UsageCoefficient: 2, InboundCoefficient: 1.5}
	if allowed, _ := s.localQuotaAllowed("wireguard", 10, "wg", policy, 10, time.Now()); !allowed {
		t.Fatal("30 effective bytes prematurely denied")
	}
	if allowed, _ := s.localQuotaAllowed("openvpn", 10, "ov", policy, 10, time.Now()); allowed {
		t.Fatal("30+30 effective bytes bypassed shared 50 byte quota")
	}
	// Panel now reflects the WG batch. Native raw for it is zero, while OV has
	// 30 effective bytes still local: global usage is still 60, not 120 or 30.
	policy.UsedTraffic, policy.ReflectedUsageBatchID = 30, "combined-reflected"
	if allowed, _ := s.localQuotaAllowed("wireguard", 10, "wg", policy, 0, time.Now()); !allowed {
		t.Fatal("old panel epoch double counted")
	}
	if allowed, _ := s.localQuotaAllowed("openvpn", 10, "ov", policy, 10, time.Now()); allowed {
		t.Fatal("ACK/reflection erased other protocol quota")
	}
	policy.UsedTraffic = 60
	if allowed, _ := s.localQuotaAllowed("openvpn", 10, "ov", policy, 0, time.Now()); allowed {
		t.Fatal("persisted usage multiplied or ignored")
	}
	other := policy
	other.UsedTraffic = 0
	if allowed, _ := s.localQuotaAllowed("ikev2", 11, "ike", other, 10, time.Now()); !allowed {
		t.Fatal("other user inherited shared quota")
	}
}
