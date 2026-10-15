package nodeagent

import (
	"fmt"
	"net/netip"
	"strings"
)

type wireGuardPeerMutationStep struct {
	Operation string
	Peer      wireGuardKernelPeer
}

type wireGuardPeerMutationPlan struct {
	Forward  []wireGuardPeerMutationStep
	Rollback []wireGuardPeerMutationStep
}

// planWireGuardKernelMutation derives target-only forward and exact reverse
// operations from the captured kernel state. Rollback restores configurable
// peer state; transient counters and handshake state are intentionally omitted.
func planWireGuardKernelMutation(
	delta wireGuardPeerDelta,
	snapshot map[string]wireGuardKernelPeer,
	userID int64,
	globalPSK string,
) (wireGuardPeerMutationPlan, error) {
	if userID <= 0 {
		return wireGuardPeerMutationPlan{}, fmt.Errorf("user id must be positive")
	}
	plan := wireGuardPeerMutationPlan{
		Forward:  make([]wireGuardPeerMutationStep, 0, len(delta.Remove)+len(delta.Set)),
		Rollback: make([]wireGuardPeerMutationStep, 0, len(delta.Remove)+len(delta.Set)),
	}
	touched := make(map[string]bool, len(delta.Remove)+len(delta.Set))
	for _, peer := range delta.Remove {
		key := strings.TrimSpace(peer.PublicKey)
		if peer.UserID != userID || key == "" || touched[key] {
			return wireGuardPeerMutationPlan{}, fmt.Errorf("WireGuard removal contains a non-target or duplicate peer")
		}
		before, exists := snapshot[key]
		if !exists {
			return wireGuardPeerMutationPlan{}, fmt.Errorf("WireGuard removal peer %q is absent from snapshot", key)
		}
		if before.PublicKey != key {
			return wireGuardPeerMutationPlan{}, fmt.Errorf("WireGuard snapshot key mismatch for peer %q", key)
		}
		touched[key] = true
		plan.Forward = append(plan.Forward, wireGuardPeerMutationStep{Operation: "remove", Peer: before})
		plan.Rollback = append([]wireGuardPeerMutationStep{{Operation: "set", Peer: cloneWireGuardKernelPeer(before)}}, plan.Rollback...)
	}
	for _, peer := range delta.Set {
		key := strings.TrimSpace(peer.PublicKey)
		if peer.UserID != userID || key == "" || touched[key] {
			return wireGuardPeerMutationPlan{}, fmt.Errorf("WireGuard set contains a non-target or duplicate peer")
		}
		address, err := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if err != nil || !address.Is4() {
			return wireGuardPeerMutationPlan{}, fmt.Errorf("WireGuard peer %q has an invalid IPv4 address", key)
		}
		psk := strings.TrimSpace(peer.PresharedKey)
		if psk == "" {
			psk = strings.TrimSpace(globalPSK)
		}
		after := wireGuardKernelPeer{PublicKey: key, PresharedKey: psk, AllowedIPs: []string{address.String() + "/32"}}
		before, existed := snapshot[key]
		if existed {
			after.Endpoint = before.Endpoint
			after.PersistentKeepalive = before.PersistentKeepalive
			plan.Rollback = append([]wireGuardPeerMutationStep{{Operation: "set", Peer: cloneWireGuardKernelPeer(before)}}, plan.Rollback...)
		} else {
			plan.Rollback = append([]wireGuardPeerMutationStep{{Operation: "remove", Peer: wireGuardKernelPeer{PublicKey: key}}}, plan.Rollback...)
		}
		touched[key] = true
		plan.Forward = append(plan.Forward, wireGuardPeerMutationStep{Operation: "set", Peer: after})
	}
	return plan, nil
}

func cloneWireGuardKernelPeer(peer wireGuardKernelPeer) wireGuardKernelPeer {
	peer.AllowedIPs = append([]string(nil), peer.AllowedIPs...)
	return peer
}

func (plan wireGuardPeerMutationPlan) RollbackForAttempted(attempted int) []wireGuardPeerMutationStep {
	if attempted < 0 {
		attempted = 0
	}
	if attempted > len(plan.Rollback) {
		attempted = len(plan.Rollback)
	}
	return append([]wireGuardPeerMutationStep(nil), plan.Rollback[len(plan.Rollback)-attempted:]...)
}
