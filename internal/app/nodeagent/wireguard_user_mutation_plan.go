package nodeagent

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type wireGuardPeerDelta struct {
	Current []wireGuardRuntimePeer
	Remove  []wireGuardRuntimePeer
	Set     []wireGuardRuntimePeer
	Peers   []wireGuardRuntimePeer
}

// planWireGuardUserPeerDelta computes a mutation limited to one panel user.
// The merged peer slice is validated as a whole before any kernel command runs.
func planWireGuardUserPeerDelta(current []wireGuardRuntimePeer, userID int64, operation string, desired []wireGuardRuntimePeer) (wireGuardPeerDelta, error) {
	if userID <= 0 {
		return wireGuardPeerDelta{}, fmt.Errorf("user id must be positive")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return wireGuardPeerDelta{}, fmt.Errorf("unsupported WireGuard user operation %q", operation)
	}
	desiredByKey := make(map[string]wireGuardRuntimePeer, len(desired))
	for _, peer := range desired {
		key := strings.TrimSpace(peer.PublicKey)
		if peer.UserID != userID || key == "" {
			return wireGuardPeerDelta{}, fmt.Errorf("desired peer identity does not match user %d", userID)
		}
		if _, exists := desiredByKey[key]; exists {
			return wireGuardPeerDelta{}, fmt.Errorf("duplicate desired WireGuard peer key")
		}
		desiredByKey[key] = peer
	}
	if operation == "remove" && len(desired) != 0 {
		return wireGuardPeerDelta{}, fmt.Errorf("remove operation must not include desired peers")
	}
	if operation == "add" && len(desired) == 0 {
		return wireGuardPeerDelta{}, fmt.Errorf("add operation requires at least one desired peer")
	}
	if operation == "update" && len(desired) == 0 {
		return wireGuardPeerDelta{}, fmt.Errorf("update operation requires at least one desired peer")
	}

	delta := wireGuardPeerDelta{
		Current: append([]wireGuardRuntimePeer(nil), current...),
		Peers:   make([]wireGuardRuntimePeer, 0, len(current)+len(desired)),
	}
	currentByKey := make(map[string]wireGuardRuntimePeer, len(current))
	for _, peer := range current {
		key := strings.TrimSpace(peer.PublicKey)
		if key == "" {
			return wireGuardPeerDelta{}, fmt.Errorf("current WireGuard policy contains an empty peer key")
		}
		if _, exists := currentByKey[key]; exists {
			return wireGuardPeerDelta{}, fmt.Errorf("current WireGuard policy contains duplicate peer keys")
		}
		currentByKey[key] = peer
		if peer.UserID != userID {
			if _, conflict := desiredByKey[key]; conflict {
				return wireGuardPeerDelta{}, fmt.Errorf("desired WireGuard peer key belongs to another user")
			}
			delta.Peers = append(delta.Peers, peer)
			continue
		}
		want, keep := desiredByKey[key]
		if !keep {
			delta.Remove = append(delta.Remove, peer)
			continue
		}
		delta.Peers = append(delta.Peers, want)
		if !wireGuardPeerKernelEqual(peer, want) {
			delta.Set = append(delta.Set, want)
		}
		delete(desiredByKey, key)
	}
	if operation == "update" {
		found := false
		for _, peer := range current {
			if peer.UserID == userID {
				found = true
				break
			}
		}
		if !found {
			return wireGuardPeerDelta{}, fmt.Errorf("WireGuard user %d does not exist", userID)
		}
	}
	for _, peer := range desiredByKey {
		delta.Set = append(delta.Set, peer)
		delta.Peers = append(delta.Peers, peer)
	}
	addresses := make(map[string]string, len(delta.Peers))
	for _, peer := range delta.Peers {
		key := strings.TrimSpace(peer.PublicKey)
		address, err := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if err != nil || !address.Is4() {
			return wireGuardPeerDelta{}, fmt.Errorf("peer %q has an invalid IPv4 address", key)
		}
		addressKey := address.String()
		if previous, exists := addresses[addressKey]; exists && previous != key {
			return wireGuardPeerDelta{}, fmt.Errorf("peer address %s is assigned to multiple keys", addressKey)
		}
		addresses[addressKey] = key
	}
	sort.Slice(delta.Remove, func(i, j int) bool { return delta.Remove[i].PublicKey < delta.Remove[j].PublicKey })
	sort.Slice(delta.Set, func(i, j int) bool { return delta.Set[i].PublicKey < delta.Set[j].PublicKey })
	sort.Slice(delta.Peers, func(i, j int) bool {
		if delta.Peers[i].UserID != delta.Peers[j].UserID {
			return delta.Peers[i].UserID < delta.Peers[j].UserID
		}
		return delta.Peers[i].PublicKey < delta.Peers[j].PublicKey
	})
	return delta, nil
}

func wireGuardPeerKernelEqual(left, right wireGuardRuntimePeer) bool {
	return strings.TrimSpace(left.PublicKey) == strings.TrimSpace(right.PublicKey) &&
		strings.TrimSpace(left.PresharedKey) == strings.TrimSpace(right.PresharedKey) &&
		strings.TrimSpace(left.Address) == strings.TrimSpace(right.Address)
}
