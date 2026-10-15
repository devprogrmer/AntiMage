package nodecontroller

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGRPCWireGuardMutationUserCarriesFullDesiredPeerSet(t *testing.T) {
	want := []WGRuntimePeer{
		{UserID: 42, DeviceIndex: 0, Username: "alice", PublicKey: "public-a", PresharedKey: "psk-a", Address: "10.69.0.2", Status: "active", DeviceLimit: 2, IPLimit: 1},
		{UserID: 42, DeviceIndex: 1, Username: "alice", PublicKey: "public-b", PresharedKey: "psk-b", Address: "10.69.0.3", Status: "active", DeviceLimit: 2, IPLimit: 1},
	}
	got, err := grpcWireGuardMutationUser(42, "update", want)
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "wireguard" || got.Fields[grpcWireGuardUserIDField] != "42" || got.Fields[grpcWireGuardOpField] != "update" {
		t.Fatalf("mutation envelope = %+v", got)
	}
	var peers []WGRuntimePeer
	if err := json.Unmarshal([]byte(got.Fields[grpcWireGuardPeersField]), &peers); err != nil {
		t.Fatal(err)
	}
	if len(peers) != len(want) {
		t.Fatalf("desired peers = %d, want %d", len(peers), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(peers[i], want[i]) {
			t.Fatalf("peer[%d] = %+v, want %+v", i, peers[i], want[i])
		}
	}
}

func TestGRPCWireGuardMutationUserRemoveHasExplicitOperationAndNoPeers(t *testing.T) {
	got, err := grpcWireGuardMutationUser(99, "remove", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "wireguard" || got.Fields[grpcWireGuardUserIDField] != "99" || got.Fields[grpcWireGuardOpField] != "remove" || got.Fields[grpcWireGuardPeersField] != "null" {
		t.Fatalf("remove envelope = %+v", got)
	}
}

func TestGRPCWireGuardMutationUserRejectsMismatchedPeer(t *testing.T) {
	if _, err := grpcWireGuardMutationUser(42, "add", []WGRuntimePeer{{UserID: 43, PublicKey: "other", Address: "10.69.0.2"}}); err == nil {
		t.Fatal("mismatched peer user was accepted")
	}
}
