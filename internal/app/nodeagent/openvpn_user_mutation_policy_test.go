package nodeagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanOpenVPNUserPolicyMutationIsolatesTargetAndIsIdempotent(t *testing.T) {
	before := nativeRuntimePayload{
		OpenVPNInbounds: []openVPNRuntimeInbound{{
			Tag: "vpn-a",
			Users: []openVPNRuntimeUser{
				{UserID: 1, Username: "alice", VPNUsername: "alice", Password: "old", Status: "active"},
				{UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-pass", Status: "active"},
			},
		}},
		L2TPInbounds: []l2TPRuntimeInbound{{Tag: "unrelated"}},
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	desired := openVPNRuntimeUser{UserID: 1, Username: "alice", VPNUsername: "alice", Password: "new", Status: "active"}
	updated, changed, err := planOpenVPNUserPolicyMutation(string(raw), "vpn-a", 1, "update", &desired)
	if err != nil || !changed {
		t.Fatalf("plan update changed=%v err=%v", changed, err)
	}
	var got nativeRuntimePayload
	if err := json.Unmarshal([]byte(updated), &got); err != nil {
		t.Fatal(err)
	}
	if got.OpenVPNInbounds[0].Users[0].Password != "new" || got.OpenVPNInbounds[0].Users[1] != before.OpenVPNInbounds[0].Users[1] {
		t.Fatalf("update changed unrelated OpenVPN users: %+v", got.OpenVPNInbounds[0].Users)
	}
	if len(got.L2TPInbounds) != 1 || got.L2TPInbounds[0].Tag != "unrelated" {
		t.Fatalf("update changed unrelated protocol state: %+v", got.L2TPInbounds)
	}
	updatedAgain, changed, err := planOpenVPNUserPolicyMutation(updated, "vpn-a", 1, "update", &desired)
	if err != nil || changed || updatedAgain != updated {
		t.Fatalf("identical update was not an idempotent no-op: changed=%v err=%v", changed, err)
	}
}

func TestPlanOpenVPNUserPolicyMutationRejectsIdentityConflicts(t *testing.T) {
	raw := `{"inbounds":[{"tag":"vpn-a","users":[{"user_id":2,"username":"bob","vpn_username":"bob","password":"secret","status":"active"}]}]}`
	desired := openVPNRuntimeUser{UserID: 1, Username: "alice", VPNUsername: "bob", Password: "alice-pass", Status: "active"}
	if _, _, err := planOpenVPNUserPolicyMutation(raw, "vpn-a", 1, "add", &desired); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate VPN username error=%v", err)
	}
	updateMissing := desired
	updateMissing.VPNUsername = "alice"
	if _, _, err := planOpenVPNUserPolicyMutation(raw, "vpn-a", 1, "update", &updateMissing); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("update absent user error=%v", err)
	}
	if _, _, err := planOpenVPNUserPolicyMutation(raw, "missing", 1, "add", &desired); err == nil || !strings.Contains(err.Error(), "not in committed policy") {
		t.Fatalf("unknown inbound error=%v", err)
	}
}

func TestPlanOpenVPNUserPolicyRemoveIsIdempotent(t *testing.T) {
	raw := `{"inbounds":[{"tag":"vpn-a","users":[{"user_id":1,"username":"alice","vpn_username":"alice","password":"secret","status":"active"}]}]}`
	updated, changed, err := planOpenVPNUserPolicyMutation(raw, "vpn-a", 1, "remove", nil)
	if err != nil || !changed {
		t.Fatalf("remove changed=%v err=%v", changed, err)
	}
	var payload nativeRuntimePayload
	if err := json.Unmarshal([]byte(updated), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.OpenVPNInbounds[0].Users) != 0 {
		t.Fatalf("user remained after remove: %+v", payload.OpenVPNInbounds[0].Users)
	}
	again, changed, err := planOpenVPNUserPolicyMutation(updated, "vpn-a", 1, "remove", nil)
	if err != nil || changed || again != updated {
		t.Fatalf("repeated remove changed=%v err=%v", changed, err)
	}
}
