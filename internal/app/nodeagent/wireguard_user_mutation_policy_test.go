package nodeagent

import (
	"encoding/json"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestDecodeWireGuardMutationUser(t *testing.T) {
	user := &nodev1.InboundUser{Protocol: "wireguard", Fields: map[string]string{
		wireGuardMutationUserIDField: "7",
		wireGuardMutationOpField:     "update",
		wireGuardMutationPeersField:  `[{"user_id":7,"public_key":"peer","address":"10.69.0.7"}]`,
	}}
	id, operation, peers, err := decodeWireGuardMutationUser(user)
	if err != nil {
		t.Fatal(err)
	}
	if id != 7 || operation != "update" || len(peers) != 1 || peers[0].Address != "10.69.0.7" {
		t.Fatalf("decoded mutation = id:%d op:%q peers:%+v", id, operation, peers)
	}
	user.Fields[wireGuardMutationPeersField] = `[{"user_id":8,"public_key":"peer","address":"10.69.0.7"}]`
	if _, _, _, err := decodeWireGuardMutationUser(user); err == nil {
		t.Fatal("accepted desired peer belonging to another user")
	}
}

func TestPlanWireGuardNativePolicyMutationIsTargetScoped(t *testing.T) {
	current := wireGuardRuntimePeer{UserID: 8, PublicKey: "other", Address: "10.69.0.8"}
	for _, tc := range []struct {
		name      string
		operation string
		desired   []wireGuardRuntimePeer
	}{
		{name: "add", operation: "add", desired: []wireGuardRuntimePeer{{UserID: 7, PublicKey: "target", Address: "10.69.0.7"}}},
		{name: "update", operation: "update", desired: []wireGuardRuntimePeer{{UserID: 7, PublicKey: "target", Address: "10.69.0.9"}}},
		{name: "remove", operation: "remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := append([]wireGuardRuntimePeer(nil), current)
			if tc.operation != "add" {
				seed = append(seed, wireGuardRuntimePeer{UserID: 7, PublicKey: "target", Address: "10.69.0.7"})
			}
			input := `{"wg_inbounds":[{"tag":"wg-main","listen_port":51820,"peers":` + mustJSONPeers(t, seed) + `}]}`
			updated, delta, err := planWireGuardNativePolicyMutation(input, "wg-main", 7, tc.operation, tc.desired)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := parseNativeRuntimePayload(updated)
			if err != nil {
				t.Fatal(err)
			}
			peers := payload.WireGuardInbounds[0].Peers
			otherPreserved := false
			for _, peer := range peers {
				if peer.UserID == 8 && peer.PublicKey == "other" && peer.Address == "10.69.0.8" {
					otherPreserved = true
				}
			}
			if !otherPreserved {
				t.Fatalf("unrelated peer was lost or changed: %+v", peers)
			}
			if tc.operation == "remove" && len(peers) != 1 {
				t.Fatalf("remove retained target peer: %+v", peers)
			}
			if tc.operation != "remove" {
				var target *wireGuardRuntimePeer
				for i := range peers {
					if peers[i].UserID == 7 {
						target = &peers[i]
					}
				}
				if len(peers) != 2 || target == nil {
					t.Fatalf("target peer missing: %+v", peers)
				}
				if tc.operation == "update" && target.Address != "10.69.0.9" {
					t.Fatalf("target update not reflected in policy: %+v", target)
				}
			}
			if len(delta.Peers) != len(peers) {
				t.Fatalf("delta peers=%d policy peers=%d", len(delta.Peers), len(peers))
			}
		})
	}
}

func mustJSONPeers(t *testing.T, peers []wireGuardRuntimePeer) string {
	t.Helper()
	raw, err := json.Marshal(peers)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPlanWireGuardNativePolicyMutationRejectsStaleOrConflictingInput(t *testing.T) {
	raw := `{"wg_inbounds":[{"tag":"wg-main","peers":[{"user_id":8,"public_key":"other","address":"10.69.0.8"}]}]}`
	for _, tc := range []struct {
		name string
		tag  string
		want []wireGuardRuntimePeer
	}{
		{name: "missing inbound", tag: "old-tag", want: []wireGuardRuntimePeer{{UserID: 7, PublicKey: "target", Address: "10.69.0.7"}}},
		{name: "address conflict", tag: "wg-main", want: []wireGuardRuntimePeer{{UserID: 7, PublicKey: "target", Address: "10.69.0.8"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated, _, err := planWireGuardNativePolicyMutation(raw, tc.tag, 7, "add", tc.want)
			if err == nil {
				t.Fatal("expected mutation planning error")
			}
			if updated != raw {
				t.Fatal("policy bytes changed after rejected mutation")
			}
		})
	}
}
