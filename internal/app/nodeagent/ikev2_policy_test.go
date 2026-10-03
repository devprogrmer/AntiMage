package nodeagent

import (
	"strings"
	"testing"
	"time"
)

func TestIKEv2PolicyReasonQuota(t *testing.T) {
	limit := int64(1000)

	user := ikev2RuntimeUser{
		Status:      "active",
		UsedTraffic: 900,
		DataLimit:   &limit,
	}

	got := ikev2PolicyReason(
		user,
		time.Unix(2000, 0),
		100,
	)

	if got != "data limit reached" {
		t.Fatalf(
			"reason=%q",
			got,
		)
	}
}

func TestIKEv2PolicyReasonExpired(t *testing.T) {
	expire := int64(1999)

	user := ikev2RuntimeUser{
		Status: "active",
		Expire: &expire,
	}

	got := ikev2PolicyReason(
		user,
		time.Unix(2000, 0),
		0,
	)

	if got != "expired" {
		t.Fatalf(
			"reason=%q",
			got,
		)
	}
}

func TestRenderIKEv2SpeedRules(t *testing.T) {
	raw := renderIKEv2SpeedRules(
		[]ikev2SpeedBinding{
			{
				UserID:       1,
				IPv4:         "10.70.0.2",
				UploadRate:   3000000,
				DownloadRate: 3000000,
			},
		},
	)

	for _, needle := range []string{
		"ip saddr 10.70.0.2",
		"ip daddr 10.70.0.2",
		"375000 bytes/second",
	} {
		if !strings.Contains(
			raw,
			needle,
		) {
			t.Fatalf(
				"missing %q:\n%s",
				needle,
				raw,
			)
		}
	}
}

func TestIKEv2PolicyPreservesLimitsAndSpeedBindings(t *testing.T) {
	user := ikev2RuntimeUser{UserID: 42, Status: "active", DeviceLimit: 3, IPLimit: 2, UploadSpeedLimit: 1234, DownloadSpeedLimit: 5678}
	if user.DeviceLimit != 3 || user.IPLimit != 2 {
		t.Fatalf("IKEv2 limits changed: %+v", user)
	}
	bindings := []ikev2SpeedBinding{{UserID: user.UserID, IPv4: "10.70.0.42", UploadRate: user.UploadSpeedLimit, DownloadRate: user.DownloadSpeedLimit}}
	if bindings[0].UploadRate != 1234 || bindings[0].DownloadRate != 5678 {
		t.Fatalf("IKEv2 speed binding lost user limits: %+v", bindings[0])
	}
}
