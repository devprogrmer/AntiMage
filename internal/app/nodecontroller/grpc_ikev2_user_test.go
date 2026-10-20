package nodecontroller

import (
	"encoding/json"
	"testing"

	"github.com/antimage/antimage/internal/app/nodeclient"
)

func TestIKEv2MutationRequestCarriesDesiredStateAndRevision(t *testing.T) {
	user := RemoteAccessRuntimeUser{UserID: 7, Username: "alice", Password: "secret", Status: "active", UsedTraffic: 123, UsageCoefficient: 1.5, InboundCoefficient: 2}
	req, err := grpcIKEv2MutationUser(user, "update", 91)
	if err != nil {
		t.Fatal(err)
	}
	f := req.GetFields()
	if req.GetProtocol() != "ikev2" || f[grpcIKEv2UserIDField] != "7" || f[grpcIKEv2OperationField] != "update" || f[grpcIKEv2RevisionField] != "91" {
		t.Fatalf("envelope=%v", f)
	}
	var got RemoteAccessRuntimeUser
	if err := json.Unmarshal([]byte(f[grpcIKEv2UserJSONField]), &got); err != nil || got.UserID != 7 || got.Password != "secret" || got.UsageCoefficient != 1.5 {
		t.Fatalf("decoded=%+v err=%v", got, err)
	}
}

func TestIKEv2CapabilityMappingRemainsExplicitAndControllerFallbackIsConservative(t *testing.T) {
	if got := targetedUserCapability("ikev2", "update_user"); got != "targeted_user_update_ikev2" {
		t.Fatalf("update capability=%q", got)
	}
	if got := targetedUserCapability("ikev2", "add_user"); got != "targeted_user_add_remove_ikev2" {
		t.Fatalf("add capability=%q", got)
	}
	if supportsTargetedUserMutation(&nodeclient.Client{}, []string{"ikev2"}, "add_user", true) {
		t.Fatal("unhandshaken node was targeted")
	}
}

func TestIKEv2LegacyNodeUsesFallbackAndCapableNodeUsesTargetedOnly(t *testing.T) {
	legacy := &nodeclient.Client{}
	if supportsTargetedUserMutation(legacy, []string{"ikev2"}, "add_user", true) {
		t.Fatal("legacy IKEv2 node selected targeted path")
	}
	capable := &nodeclient.Client{}
	capable.SetHandshake("test", []string{"targeted_user_add_remove_ikev2", "targeted_user_update_ikev2"})
	if !supportsTargetedUserMutation(capable, []string{"ikev2"}, "add_user", true) {
		t.Fatal("capable IKEv2 node did not select targeted add")
	}
	if !supportsTargetedUserMutation(capable, []string{"ikev2"}, "update_user", true) {
		t.Fatal("capable IKEv2 node did not select targeted update")
	}
	if supportsTargetedUserMutation(capable, []string{"ikev2", "openvpn"}, "add_user", true) {
		t.Fatal("mixed protocol mutation was not conservative")
	}
}
