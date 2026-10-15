package nodecontroller

import (
	"encoding/json"
	"testing"
)

func TestGRPCAmneziaWGMutationUserEncodesScopedOperation(t *testing.T) {
	peers := []AWGRuntimePeer{{UserID: 7, PublicKey: "public-key", Address: "10.72.0.7"}}
	user, err := grpcAmneziaWGMutationUser(7, "add", peers)
	if err != nil {
		t.Fatal(err)
	}
	if user.GetProtocol() != "amneziawg" || user.GetFields()[grpcAmneziaWGUserIDField] != "7" || user.GetFields()[grpcAmneziaWGOpField] != "add" {
		t.Fatalf("encoded AWG operation envelope=%+v", user)
	}
	var decoded []AWGRuntimePeer
	if err := json.Unmarshal([]byte(user.GetFields()[grpcAmneziaWGPeersField]), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0] != peers[0] {
		t.Fatalf("encoded AWG peers=%+v, want %+v", decoded, peers)
	}
}

func TestGRPCAmneziaWGMutationUserRejectsWrongOwner(t *testing.T) {
	if user, err := grpcAmneziaWGMutationUser(7, "update", []AWGRuntimePeer{{UserID: 8, PublicKey: "public-key", Address: "10.72.0.8"}}); err == nil || user != nil {
		t.Fatalf("wrong-owner AWG peer accepted: user=%v err=%v", user, err)
	}
}
