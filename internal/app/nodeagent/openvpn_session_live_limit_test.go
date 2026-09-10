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
