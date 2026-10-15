package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AWG peer edits use wgctrl's single-peer configuration API. AWG-specific
// obfuscation and interface settings are intentionally outside this boundary.
var (
	amneziaWGPeerSnapshot = amneziaWGPlatformPeerSnapshot
	amneziaWGPeerApply    = amneziaWGPlatformPeerApply
)

func executeAmneziaWGPeerMutationPlan(ctx context.Context, interfaceName string, plan wireGuardPeerMutationPlan) error {
	rollbackAttempted := func(attempted int) error {
		var rollbackErr error
		for _, rollback := range plan.RollbackForAttempted(attempted) {
			if err := amneziaWGPeerApply(interfaceName, rollback); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback AmneziaWG peer %q: %w", rollback.Peer.PublicKey, err))
			}
		}
		return rollbackErr
	}
	attempted := 0
	for _, step := range plan.Forward {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, rollbackAttempted(attempted))
		}
		attempted++
		if err := amneziaWGPeerApply(interfaceName, step); err != nil {
			return errors.Join(fmt.Errorf("apply AmneziaWG peer %q: %w", step.Peer.PublicKey, err), rollbackAttempted(attempted))
		}
	}
	return ctx.Err()
}

func planAmneziaWGUserPeerDelta(current []amneziaWGRuntimePeer, userID int64, operation string, desired []amneziaWGRuntimePeer) (wireGuardPeerDelta, error) {
	toWireGuard := func(peers []amneziaWGRuntimePeer) []wireGuardRuntimePeer {
		result := make([]wireGuardRuntimePeer, 0, len(peers))
		for _, peer := range peers {
			result = append(result, wireGuardRuntimePeer{
				UserID: peer.UserID, DeviceIndex: peer.DeviceIndex, Username: peer.Username,
				PublicKey: peer.PublicKey, PresharedKey: peer.PresharedKey, Address: peer.Address,
				Status: peer.Status, UsedTraffic: peer.UsedTraffic, DataLimit: peer.DataLimit,
				Expire: peer.Expire, DeviceLimit: peer.DeviceLimit, IPLimit: peer.IPLimit,
				UploadSpeedLimit: peer.UploadSpeedLimit, DownloadSpeedLimit: peer.DownloadSpeedLimit,
				UsageCoefficient: peer.UsageCoefficient, InboundCoefficient: peer.InboundCoefficient,
			})
		}
		return result
	}
	delta, err := planWireGuardUserPeerDelta(toWireGuard(current), userID, operation, toWireGuard(desired))
	if err != nil {
		return wireGuardPeerDelta{}, err
	}
	return delta, nil
}

func planAmneziaWGNativePolicyMutation(raw, inboundTag string, userID int64, operation string, desired []amneziaWGRuntimePeer) (string, wireGuardPeerDelta, error) {
	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("read committed AmneziaWG policy: %w", err)
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("AmneziaWG inbound tag is required")
	}
	matched := -1
	for i := range payload.AmneziaWGInbounds {
		if strings.TrimSpace(payload.AmneziaWGInbounds[i].Tag) != inboundTag {
			continue
		}
		if matched >= 0 {
			return raw, wireGuardPeerDelta{}, fmt.Errorf("AmneziaWG inbound tag %q is ambiguous", inboundTag)
		}
		matched = i
	}
	if matched < 0 {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("AmneziaWG inbound %q is not in committed policy", inboundTag)
	}
	inbound := payload.AmneziaWGInbounds[matched]
	delta, err := planAmneziaWGUserPeerDelta(inbound.Peers, userID, operation, desired)
	if err != nil {
		return raw, wireGuardPeerDelta{}, err
	}
	inbound.Peers = amneziaWGPeersFromWireGuard(delta.Peers)
	payload.AmneziaWGInbounds[matched] = inbound
	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, wireGuardPeerDelta{}, fmt.Errorf("encode updated AmneziaWG policy: %w", err)
	}
	return string(updated), delta, nil
}

func amneziaWGMutationUsageConfig(old amneziaWGUsageRuntimeConfig, userID int64, desired []amneziaWGRuntimePeer) (amneziaWGUsageRuntimeConfig, error) {
	if userID <= 0 {
		return amneziaWGUsageRuntimeConfig{}, fmt.Errorf("AmneziaWG mutation user id must be positive")
	}
	updated := old
	updated.PeerGenerations = cloneAmneziaWGMap(old.PeerGenerations)
	updated.Peers = cloneAmneziaWGMap(old.Peers)
	updated.PeerAddresses = cloneAmneziaWGMap(old.PeerAddresses)
	updated.Policies = cloneAmneziaWGMap(old.Policies)
	if updated.PeerGenerations == nil {
		updated.PeerGenerations = map[string]string{}
	}
	if updated.Peers == nil {
		updated.Peers = map[string]int64{}
	}
	if updated.PeerAddresses == nil {
		updated.PeerAddresses = map[string]string{}
	}
	if updated.Policies == nil {
		updated.Policies = map[string]nativeSessionUserPolicy{}
	}
	previousOwners := cloneAmneziaWGMap(updated.Peers)
	previousGenerations := cloneAmneziaWGMap(updated.PeerGenerations)
	for key, owner := range updated.Peers {
		if owner != userID {
			continue
		}
		delete(updated.Peers, key)
		delete(updated.PeerAddresses, key)
		delete(updated.Policies, key)
		delete(updated.PeerGenerations, key)
	}
	for _, peer := range desired {
		key := strings.TrimSpace(peer.PublicKey)
		if peer.UserID != userID || key == "" {
			return amneziaWGUsageRuntimeConfig{}, fmt.Errorf("AmneziaWG desired peer identity mismatch")
		}
		generation := ""
		if previousOwners[key] == userID {
			generation = strings.TrimSpace(previousGenerations[key])
		}
		if generation == "" {
			var err error
			generation, err = amneziaWGMutationGeneration()
			if err != nil {
				return amneziaWGUsageRuntimeConfig{}, err
			}
		}
		updated.Peers[key] = userID
		updated.PeerAddresses[key] = strings.TrimSpace(peer.Address)
		updated.Policies[key] = amneziaWGPeerPolicy(peer)
		updated.PeerGenerations[key] = generation
	}
	updated.Transitioning = false
	return updated, nil
}

func amneziaWGMutationGeneration() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", token[:]), nil
}

func amneziaWGPeersFromWireGuard(peers []wireGuardRuntimePeer) []amneziaWGRuntimePeer {
	result := make([]amneziaWGRuntimePeer, 0, len(peers))
	for _, peer := range peers {
		result = append(result, amneziaWGRuntimePeer{
			ReflectedUsageBatchID: peer.ReflectedUsageBatchID, UserID: peer.UserID,
			Username: peer.Username, DeviceIndex: peer.DeviceIndex, PublicKey: peer.PublicKey,
			PresharedKey: peer.PresharedKey, Address: peer.Address, Status: peer.Status,
			UsedTraffic: peer.UsedTraffic, DataLimit: peer.DataLimit, Expire: peer.Expire,
			DeviceLimit: peer.DeviceLimit, IPLimit: peer.IPLimit,
			UploadSpeedLimit: peer.UploadSpeedLimit, DownloadSpeedLimit: peer.DownloadSpeedLimit,
			UsageCoefficient: peer.UsageCoefficient, InboundCoefficient: peer.InboundCoefficient,
		})
	}
	return result
}

func wireGuardPeerSnapshotMatchesAmneziaPolicy(snapshot map[string]wireGuardKernelPeer, peers []amneziaWGRuntimePeer) error {
	converted := make([]wireGuardRuntimePeer, 0, len(peers))
	for _, peer := range peers {
		converted = append(converted, wireGuardRuntimePeer{PublicKey: peer.PublicKey, PresharedKey: peer.PresharedKey, Address: peer.Address})
	}
	return wireGuardPeerSnapshotMatchesPolicy(snapshot, converted, "")
}

func amneziaWGKernelPlan(delta wireGuardPeerDelta, snapshot map[string]wireGuardKernelPeer, userID int64) (wireGuardPeerMutationPlan, error) {
	return planWireGuardKernelMutation(delta, snapshot, userID, "")
}

func amneziaWGInboundConfigPath(dataDir, inboundTag string) string {
	return filepath.Join(dataDir, "amneziawg", "runtime", amneziaWGRuntimeDirName(inboundTag), "interface.conf")
}
