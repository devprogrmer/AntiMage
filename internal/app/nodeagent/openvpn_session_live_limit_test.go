package nodeagent

import (
	"testing"
	"time"
)

func TestNativeSessionUserPolicyAllowedWithLiveUsage(
	t *testing.T,
) {
	now := time.Unix(2_000_000_000, 0)

	policy := nativeSessionUserPolicy{
		Status:      "active",
		UsedTraffic: 900,
		DataLimit:   1000,
	}

	allowed, _ :=
		nativeSessionUserPolicyAllowedWithLiveUsage(
			policy,
			99,
			now,
		)

	if !allowed {
		t.Fatal("user below live data limit was denied")
	}

	allowed, reason :=
		nativeSessionUserPolicyAllowedWithLiveUsage(
			policy,
			100,
			now,
		)

	if allowed {
		t.Fatal("user at live data limit was allowed")
	}

	if reason != "data limit reached" {
		t.Fatalf(
			"unexpected reason: %q",
			reason,
		)
	}

	onHold := nativeSessionUserPolicy{
		Status:      "on_hold",
		UsedTraffic: 999,
		DataLimit:   1000,
	}

	allowed, _ =
		nativeSessionUserPolicyAllowedWithLiveUsage(
			onHold,
			10000,
			now,
		)

	if !allowed {
		t.Fatal(
			"on_hold user must preserve existing auth semantics",
		)
	}
}

func TestNativeSessionPolicyLiveUsageUsesEffectiveCoefficients(
	t *testing.T,
) {
	policy := nativeSessionUserPolicy{
		Status:             "active",
		UsedTraffic:        900,
		DataLimit:          1000,
		UsageCoefficient:   1.5,
		InboundCoefficient: 2,
	}

	allowed, reason := nativeSessionUserPolicyAllowedWithLiveUsage(
		policy,
		34,
		time.Now().UTC(),
	)
	if allowed {
		t.Fatalf("expected effective live usage to exceed quota")
	}
	if reason != "data limit reached" {
		t.Fatalf("reason = %q", reason)
	}

	rawAllowed, _ := nativeSessionUserPolicyAllowedWithLiveUsage(
		nativeSessionUserPolicy{
			Status:      "active",
			UsedTraffic: 900,
			DataLimit:   1000,
		},
		34,
		time.Now().UTC(),
	)
	if !rawAllowed {
		t.Fatal("raw live usage without coefficients should remain below quota")
	}
}
