package nodeagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanL2TPUserPolicyMutationAddUpdateRemove(t *testing.T) {
	base := l2TPRuntimeInbound{Tag: "l2tp-main", Port: 1701,
		Settings: map[string]any{"ipv4_pool_cidr": "10.67.0.0/24", "ipsec_psk": "psk"},
		Users:    []l2TPRuntimeUser{{UserID: 7, Username: "primary", VPNUsername: "primary-vpn", Password: "keep", IPv4Address: "10.67.0.10", Status: "active"}}}
	raw := encodeL2TPPolicyForTest(t, base)
	target := l2TPRuntimeUser{UserID: 8, Username: "target", VPNUsername: "target-vpn", Password: "secret", IPv4Address: "10.67.0.11", Status: "active"}

	added, changed, err := planL2TPUserPolicyMutation(raw, base.Tag, target.UserID, "add", &target)
	if err != nil || !changed {
		t.Fatalf("add changed=%v err=%v", changed, err)
	}
	assertL2TPPolicyUsers(t, added, base.Tag, []int64{7, 8})
	if retry, changed, err := planL2TPUserPolicyMutation(added, base.Tag, target.UserID, "add", &target); err != nil || changed || retry != added {
		t.Fatalf("identical add retry changed=%v err=%v", changed, err)
	}

	updatedUser := target
	updatedUser.Password = "rotated"
	updatedUser.IPv4Address = "10.67.0.12"
	updated, changed, err := planL2TPUserPolicyMutation(added, base.Tag, target.UserID, "update", &updatedUser)
	if err != nil || !changed {
		t.Fatalf("update changed=%v err=%v", changed, err)
	}
	assertL2TPPolicyUsers(t, updated, base.Tag, []int64{7, 8})
	if !strings.Contains(updated, "rotated") || strings.Contains(updated, "10.67.0.11") {
		t.Fatalf("updated policy did not replace target credentials/address: %s", updated)
	}

	removed, changed, err := planL2TPUserPolicyMutation(updated, base.Tag, target.UserID, "remove", nil)
	if err != nil || !changed {
		t.Fatalf("remove changed=%v err=%v", changed, err)
	}
	assertL2TPPolicyUsers(t, removed, base.Tag, []int64{7})
	if retry, changed, err := planL2TPUserPolicyMutation(removed, base.Tag, target.UserID, "remove", nil); err != nil || changed || retry != removed {
		t.Fatalf("identical remove retry changed=%v err=%v", changed, err)
	}
}

func TestPlanL2TPUserPolicyMutationRejectsOwnershipAndInputConflicts(t *testing.T) {
	base := l2TPRuntimeInbound{Tag: "l2tp-main", Port: 1701,
		Settings: map[string]any{"ipv4_pool_cidr": "10.67.0.0/24", "ipsec_psk": "psk"},
		Users:    []l2TPRuntimeUser{{UserID: 7, Username: "primary", VPNUsername: "primary-vpn", Password: "keep", IPv4Address: "10.67.0.10", Status: "active"}}}
	raw := encodeL2TPPolicyForTest(t, base)
	tests := []struct {
		name string
		user l2TPRuntimeUser
		want string
	}{
		{name: "username conflict", user: l2TPRuntimeUser{UserID: 8, Username: "primary", VPNUsername: "primary-vpn", Password: "secret", IPv4Address: "10.67.0.11", Status: "active"}, want: "username conflicts"},
		{name: "address conflict", user: l2TPRuntimeUser{UserID: 8, Username: "other", VPNUsername: "other-vpn", Password: "secret", IPv4Address: "10.67.0.10", Status: "active"}, want: "IPv4 address conflicts"},
		{name: "outside pool", user: l2TPRuntimeUser{UserID: 8, Username: "other", VPNUsername: "other-vpn", Password: "secret", IPv4Address: "10.68.0.10", Status: "active"}, want: "outside the configured pool"},
		{name: "line injection", user: l2TPRuntimeUser{UserID: 8, Username: "other", VPNUsername: "other\nuser", Password: "secret", IPv4Address: "10.67.0.11", Status: "active"}, want: "line break"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, changed, err := planL2TPUserPolicyMutation(raw, base.Tag, tt.user.UserID, "add", &tt.user); err == nil || !strings.Contains(err.Error(), tt.want) || changed || got != raw {
				t.Fatalf("policy mutation got changed=%v err=%v want error containing %q", changed, err, tt.want)
			}
		})
	}
	if _, _, err := planL2TPUserPolicyMutation(raw, base.Tag, 8, "update", &l2TPRuntimeUser{UserID: 8, VPNUsername: "other", Password: "secret", Status: "active"}); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("update of missing user error=%v", err)
	}
	if _, _, err := planL2TPUserPolicyMutation(raw, base.Tag, 7, "remove", &base.Users[0]); err == nil || !strings.Contains(err.Error(), "cannot include") {
		t.Fatalf("remove with desired user error=%v", err)
	}
}

func encodeL2TPPolicyForTest(t *testing.T, inbound l2TPRuntimeInbound) string {
	t.Helper()
	payload := nativeRuntimePayload{L2TPInbounds: []l2TPRuntimeInbound{inbound}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertL2TPPolicyUsers(t *testing.T, raw, tag string, want []int64) {
	t.Helper()
	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, inbound := range payload.L2TPInbounds {
		if strings.TrimSpace(inbound.Tag) != tag {
			continue
		}
		if len(inbound.Users) != len(want) {
			t.Fatalf("L2TP policy user count=%d want %d: %+v", len(inbound.Users), len(want), inbound.Users)
		}
		for i, userID := range want {
			if inbound.Users[i].UserID != userID {
				t.Fatalf("L2TP policy user[%d]=%d want %d", i, inbound.Users[i].UserID, userID)
			}
		}
		return
	}
	t.Fatalf("L2TP inbound %q missing from policy", tag)
}
