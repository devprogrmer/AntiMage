package nodeagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanAnyConnectUserPolicyMutationIsolatesTargetAndIsIdempotent(t *testing.T) {
	before := nativeRuntimePayload{
		AnyConnectInbounds: []anyConnectRuntimeInbound{{
			Tag: "ac-a",
			Users: []anyConnectRuntimeUser{
				{UserID: 1, Username: "alice", Password: "old", Status: "active"},
				{UserID: 2, Username: "bob", Password: "bob-pass", Status: "active"},
			},
		}},
		IKEv2Inbounds: []ikev2RuntimeInbound{{Tag: "unrelated"}},
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	desired := anyConnectRuntimeUser{UserID: 1, Username: "alice", Password: "new", Status: "active"}
	updated, changed, err := planAnyConnectUserPolicyMutation(string(raw), "ac-a", 1, "update", &desired)
	if err != nil || !changed {
		t.Fatalf("plan update changed=%v err=%v", changed, err)
	}
	var got nativeRuntimePayload
	if err := json.Unmarshal([]byte(updated), &got); err != nil {
		t.Fatal(err)
	}
	if got.AnyConnectInbounds[0].Users[0].Password != "new" || got.AnyConnectInbounds[0].Users[1] != before.AnyConnectInbounds[0].Users[1] {
		t.Fatalf("update changed unrelated AnyConnect users: %+v", got.AnyConnectInbounds[0].Users)
	}
	if len(got.IKEv2Inbounds) != 1 || got.IKEv2Inbounds[0].Tag != "unrelated" {
		t.Fatalf("update changed unrelated protocol state: %+v", got.IKEv2Inbounds)
	}
	updatedAgain, changed, err := planAnyConnectUserPolicyMutation(updated, "ac-a", 1, "update", &desired)
	if err != nil || changed || updatedAgain != updated {
		t.Fatalf("identical update was not an idempotent no-op: changed=%v err=%v", changed, err)
	}
}

func TestPlanAnyConnectUserPolicyMutationAddRejectsExistingDifferentState(t *testing.T) {
	raw := `{"anyconnect_inbounds":[{"tag":"ac-a","users":[{"user_id":1,"username":"alice","password":"secret","status":"active"}]}]}`
	same := anyConnectRuntimeUser{UserID: 1, Username: "alice", Password: "secret", Status: "active"}
	if _, changed, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "add", &same); err != nil || changed {
		t.Fatalf("add of identical existing user changed=%v err=%v", changed, err)
	}
	conflicting := same
	conflicting.Password = "other"
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "add", &conflicting); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("add of conflicting existing user error=%v", err)
	}
	added := anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-pass", Status: "active"}
	updated, changed, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 2, "add", &added)
	if err != nil || !changed {
		t.Fatalf("add of new user changed=%v err=%v", changed, err)
	}
	var payload nativeRuntimePayload
	if err := json.Unmarshal([]byte(updated), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.AnyConnectInbounds[0].Users) != 2 || payload.AnyConnectInbounds[0].Users[0].Username != "alice" {
		t.Fatalf("add disturbed existing users: %+v", payload.AnyConnectInbounds[0].Users)
	}
}

func TestPlanAnyConnectUserPolicyMutationRejectsIdentityConflicts(t *testing.T) {
	raw := `{"anyconnect_inbounds":[{"tag":"ac-a","users":[{"user_id":2,"username":"bob","password":"secret","status":"active"}]}]}`
	desired := anyConnectRuntimeUser{UserID: 1, Username: "bob", Password: "alice-pass", Status: "active"}
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "add", &desired); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("duplicate username error=%v", err)
	}
	updateMissing := desired
	updateMissing.Username = "alice"
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "update", &updateMissing); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("update absent user error=%v", err)
	}
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "missing", 1, "add", &desired); err == nil || !strings.Contains(err.Error(), "not in committed policy") {
		t.Fatalf("unknown inbound error=%v", err)
	}
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "add", nil); err == nil {
		t.Fatal("add without desired user was accepted")
	}
	if _, _, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "remove", &desired); err == nil {
		t.Fatal("remove with desired user was accepted")
	}
}

func TestPlanAnyConnectUserPolicyRemoveIsIdempotent(t *testing.T) {
	raw := `{"anyconnect_inbounds":[{"tag":"ac-a","users":[{"user_id":1,"username":"alice","password":"secret","status":"active"}]}]}`
	updated, changed, err := planAnyConnectUserPolicyMutation(raw, "ac-a", 1, "remove", nil)
	if err != nil || !changed {
		t.Fatalf("remove changed=%v err=%v", changed, err)
	}
	var payload nativeRuntimePayload
	if err := json.Unmarshal([]byte(updated), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.AnyConnectInbounds[0].Users) != 0 {
		t.Fatalf("user remained after remove: %+v", payload.AnyConnectInbounds[0].Users)
	}
	again, changed, err := planAnyConnectUserPolicyMutation(updated, "ac-a", 1, "remove", nil)
	if err != nil || changed || again != updated {
		t.Fatalf("repeated remove changed=%v err=%v", changed, err)
	}
}
