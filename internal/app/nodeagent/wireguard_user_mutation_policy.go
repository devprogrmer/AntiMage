package nodeagent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	wireGuardMutationUserIDField = "wireguard_user_id"
	wireGuardMutationPeersField  = "wireguard_peers_json"
	wireGuardMutationOpField     = "wireguard_operation"
	wireGuardRemoveUserPrefix    = "wireguard-user:"
)

func wireGuardRemoveUserID(email string) (int64, bool) {
	value := strings.TrimSpace(email)
	if !strings.HasPrefix(value, wireGuardRemoveUserPrefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(value, wireGuardRemoveUserPrefix), 10, 64)
	return id, err == nil && id > 0
}

func decodeWireGuardMutationUser(user *nodev1.InboundUser) (int64, string, []wireGuardRuntimePeer, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "wireguard") {
		return 0, "", nil, fmt.Errorf("WireGuard mutation user is required")
	}
	fields := user.GetFields()
	userID, err := strconv.ParseInt(strings.TrimSpace(fields[wireGuardMutationUserIDField]), 10, 64)
	if err != nil || userID <= 0 {
		return 0, "", nil, fmt.Errorf("WireGuard mutation user id is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[wireGuardMutationOpField]))
	if operation != "add" && operation != "update" && operation != "remove" {
		return 0, "", nil, fmt.Errorf("WireGuard mutation operation is invalid")
	}
	var peers []wireGuardRuntimePeer
	rawPeers := strings.TrimSpace(fields[wireGuardMutationPeersField])
	if rawPeers != "" && rawPeers != "null" {
		if err := json.Unmarshal([]byte(rawPeers), &peers); err != nil {
			return 0, "", nil, fmt.Errorf("decode WireGuard desired peers: %w", err)
		}
	}
	for _, peer := range peers {
		if peer.UserID != userID {
			return 0, "", nil, fmt.Errorf("WireGuard desired peer belongs to another user")
		}
	}
	return userID, operation, peers, nil
}

// planWireGuardNativePolicyMutation transforms only one unambiguous inbound in
// the last committed native policy. It returns the original bytes on every
// error and performs all user/address conflict checks before publication.
func planWireGuardNativePolicyMutation(
	raw string,
	inboundTag string,
	userID int64,
	operation string,
	desired []wireGuardRuntimePeer,
) (string, wireGuardPeerDelta, error) {
	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("read committed native policy: %w", err)
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("WireGuard inbound tag is required")
	}
	matched := -1
	for i := range payload.WireGuardInbounds {
		if strings.TrimSpace(payload.WireGuardInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, wireGuardPeerDelta{}, fmt.Errorf("WireGuard inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("WireGuard inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.WireGuardInbounds[matched]
	delta, err := planWireGuardUserPeerDelta(inbound.Peers, userID, operation, desired)
	if err != nil {
		return raw, wireGuardPeerDelta{}, err
	}
	inbound.Peers = delta.Peers
	payload.WireGuardInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("encode updated native policy: %w", err)
	}
	return string(updated), delta, nil
}
