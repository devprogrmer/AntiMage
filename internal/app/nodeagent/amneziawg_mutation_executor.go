package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// AWG peer edits use wgctrl's single-peer configuration API. AWG-specific
// obfuscation and interface settings are intentionally outside this boundary.
var (
	amneziaWGPeerSnapshot = amneziaWGPlatformPeerSnapshot
	amneziaWGPeerApply    = amneziaWGPlatformPeerApply
)

func executeAmneziaWGPeerMutationPlan(ctx context.Context, interfaceName string, plan wireGuardPeerMutationPlan) error {
	attempted := 0
	for _, step := range plan.Forward {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempted++
		if err := amneziaWGPeerApply(interfaceName, step); err != nil {
			var rollbackErr error
			for _, rollback := range plan.RollbackForAttempted(attempted) {
				if err := amneziaWGPeerApply(interfaceName, rollback); err != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback AmneziaWG peer %q: %w", rollback.Peer.PublicKey, err))
				}
			}
			return errors.Join(fmt.Errorf("apply AmneziaWG peer %q: %w", step.Peer.PublicKey, err), rollbackErr)
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
	return planWireGuardUserPeerDelta(toWireGuard(current), userID, operation, toWireGuard(desired))
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
