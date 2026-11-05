package user

import (
	"math"
	"testing"
	"time"
)

func TestShopExtensionPreservesTrafficAndRemainingTime(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name                            string
		status                          UserStatus
		limit, used                     int64
		expire, hold                    *int64
		kind                            string
		wantLimit, wantExpire, wantHold int64
		wantStatus                      UserStatus
		fail                            bool
	}{
		{"active renewal", UserStatusActive, 1000, 200, ptr(now.Unix() + 86400), nil, "renew", 1100, now.Unix() + 31*86400, 0, UserStatusActive, false},
		{"expired renewal", UserStatusExpired, 1000, 200, ptr(now.Unix() - 86400), nil, "renew", 1100, now.Unix() + 30*86400, 0, UserStatusActive, false},
		{"over limit", UserStatusLimited, 1000, 1020, ptr(now.Unix() + 86400), nil, "volume", 1120, now.Unix() + 86400, 0, UserStatusActive, false},
		{"on hold", UserStatusOnHold, 1000, 0, nil, ptr(86400), "renew", 1100, 0, 31 * 86400, UserStatusOnHold, false},
		{"expired volume rejected", UserStatusExpired, 1000, 200, ptr(now.Unix() - 1), nil, "volume", 0, 0, 0, "", true},
		{"disabled", UserStatusDisabled, 1000, 200, ptr(now.Unix() + 86400), nil, "renew", 0, 0, 0, "", true},
		{"unlimited", UserStatusActive, 0, 200, nil, nil, "renew", 0, 0, 0, "", true},
		{"overflow", UserStatusActive, math.MaxInt64, 200, nil, nil, "volume", 0, 0, 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := existingUserRow{Status: tc.status, DataLimit: ptr(tc.limit), UsedTraffic: tc.used, Expire: tc.expire, OnHoldExpireDuration: tc.hold}
			payload, _, err := shopExtensionPayload(existing, 100, 30, tc.kind, now)
			if tc.fail {
				if err == nil {
					t.Fatal("invalid extension accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if int64PtrValue(payload.DataLimit) != tc.wantLimit || int64PtrValue(payload.Expire) != tc.wantExpire || int64PtrValue(payload.OnHoldExpireDuration) != tc.wantHold || UserStatus(payload.Status) != tc.wantStatus {
				t.Fatalf("payload=%+v", payload)
			}
			if existing.UsedTraffic != tc.used {
				t.Fatal("usage reset")
			}
		})
	}
}
