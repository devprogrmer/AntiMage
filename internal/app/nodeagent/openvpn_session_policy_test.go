package nodeagent

import (
	"testing"
	"time"
)

func TestNativeSessionUserPolicyAllowed(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)

	tests := []struct {
		name    string
		policy  nativeSessionUserPolicy
		allowed bool
	}{
		{
			name: "active",
			policy: nativeSessionUserPolicy{
				Status: "active",
			},
			allowed: true,
		},
		{
			name: "on hold",
			policy: nativeSessionUserPolicy{
				Status: "on_hold",
			},
			allowed: true,
		},
		{
			name: "disabled",
			policy: nativeSessionUserPolicy{
				Status: "disabled",
			},
			allowed: false,
		},
		{
			name: "expired",
			policy: nativeSessionUserPolicy{
				Status: "active",
				Expire: now.Unix(),
			},
			allowed: false,
		},
		{
			name: "data limit reached",
			policy: nativeSessionUserPolicy{
				Status:      "active",
				UsedTraffic: 1000,
				DataLimit:   1000,
			},
			allowed: false,
		},
		{
			name: "below data limit",
			policy: nativeSessionUserPolicy{
				Status:      "active",
				UsedTraffic: 999,
				DataLimit:   1000,
			},
			allowed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allowed, _ := nativeSessionUserPolicyAllowed(
				test.policy,
				now,
			)

			if allowed != test.allowed {
				t.Fatalf(
					"allowed=%v, want %v",
					allowed,
					test.allowed,
				)
			}
		})
	}
}
