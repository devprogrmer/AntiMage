package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// AWG peer edits use wgctrl's single-peer configuration API. AWG-specific
// obfuscation and interface settings are intentionally outside this boundary.
var (
	amneziaWGPeerSnapshot         = amneziaWGPlatformPeerSnapshot
	amneziaWGPeerApply            = amneziaWGPlatformPeerApply
	amneziaWGMutationWrite        = atomicWriteFile
	amneziaWGMutationDurableWrite = amneziaWGDurableWrite
)

const (
	amneziaWGMutationUserIDField = "amneziawg_user_id"
	amneziaWGMutationPeersField  = "amneziawg_peers_json"
	amneziaWGMutationOpField     = "amneziawg_operation"
	amneziaWGRemoveUserPrefix    = "amneziawg-user:"
)

func amneziaWGRemoveUserID(email string) (int64, bool) {
	value := strings.TrimSpace(email)
	if !strings.HasPrefix(value, amneziaWGRemoveUserPrefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(value, amneziaWGRemoveUserPrefix), 10, 64)
	return id, err == nil && id > 0
}

func decodeAmneziaWGMutationUser(user *nodev1.InboundUser) (int64, string, []amneziaWGRuntimePeer, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "amneziawg") {
		return 0, "", nil, fmt.Errorf("AmneziaWG mutation user is required")
	}
	fields := user.GetFields()
	userID, err := strconv.ParseInt(strings.TrimSpace(fields[amneziaWGMutationUserIDField]), 10, 64)
	if err != nil || userID <= 0 {
		return 0, "", nil, fmt.Errorf("AmneziaWG mutation user id is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[amneziaWGMutationOpField]))
	if operation != "add" && operation != "update" && operation != "remove" {
		return 0, "", nil, fmt.Errorf("AmneziaWG mutation operation is invalid")
	}
	var peers []amneziaWGRuntimePeer
	raw := strings.TrimSpace(fields[amneziaWGMutationPeersField])
	if raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &peers); err != nil {
			return 0, "", nil, fmt.Errorf("decode AmneziaWG desired peers: %w", err)
		}
	}
	for _, peer := range peers {
		if peer.UserID != userID {
			return 0, "", nil, fmt.Errorf("AmneziaWG desired peer belongs to another user")
		}
	}
	if operation == "remove" && len(peers) != 0 {
		return 0, "", nil, fmt.Errorf("AmneziaWG remove must not include desired peers")
	}
	return userID, operation, peers, nil
}

// applyAmneziaWGUserMutation is called only while the stable inbound lifecycle
// gate is held. It checkpoints accounting, mutates target peers via wgctrl,
// commits audit/helper/native policy in order, and reverses the target plan on
// every persistence failure.
func (s *Server) applyAmneziaWGUserMutation(ctx context.Context, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, envelopeOperation, desired, err := decodeAmneziaWGMutationUser(user)
	if err != nil {
		return err
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if envelopeOperation != operation {
		return fmt.Errorf("AmneziaWG operation envelope mismatch")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load AmneziaWG runtime policy: %w", err)
	}
	updatedNative, _, err := planAmneziaWGNativePolicyMutation(policy.NativeJSON, inboundTag, userID, operation, desired)
	if err != nil {
		return err
	}
	oldPayload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil {
		return err
	}
	newPayload, err := parseNativeRuntimePayload(updatedNative)
	if err != nil {
		return err
	}
	oldInbound, oldOK := findAmneziaWGInbound(oldPayload.AmneziaWGInbounds, inboundTag)
	newInbound, newOK := findAmneziaWGInbound(newPayload.AmneziaWGInbounds, inboundTag)
	if !oldOK || !newOK {
		return fmt.Errorf("AmneziaWG inbound %q disappeared from committed policy", inboundTag)
	}
	oldLive := filterAmneziaWGRuntimeInboundByPolicy(oldInbound, time.Now().UTC())
	newLive := filterAmneziaWGRuntimeInboundByPolicy(newInbound, time.Now().UTC())
	s.mu.Lock()
	state, ok := s.amneziaWGRuntimes[inboundTag]
	s.mu.Unlock()
	if !ok {
		states, loadErr := s.loadAmneziaWGRuntimeStates()
		if loadErr != nil {
			return loadErr
		}
		state, ok = states[inboundTag]
	}
	if !ok || state.Tag != inboundTag {
		return fmt.Errorf("AmneziaWG inbound %q has no managed runtime", inboundTag)
	}
	expectedName, err := amneziaWGGeneratedInterfaceName(inboundTag)
	if err != nil {
		return err
	}
	if state.InterfaceName != expectedName || filepath.Clean(state.ConfigPath) != filepath.Clean(amneziaWGInboundConfigPath(s.cfg.DataDir, inboundTag)) {
		return fmt.Errorf("AmneziaWG runtime identity does not match managed inbound")
	}
	s.amneziaWGUsageMu.Lock()
	defer s.amneziaWGUsageMu.Unlock()
	helperPath := filepath.Join(filepath.Dir(state.ConfigPath), "usage-helper.json")
	oldHelperRaw, err := os.ReadFile(helperPath)
	if err != nil {
		return fmt.Errorf("read AmneziaWG usage policy: %w", err)
	}
	var oldHelper amneziaWGUsageRuntimeConfig
	if err := json.Unmarshal(oldHelperRaw, &oldHelper); err != nil {
		return fmt.Errorf("decode AmneziaWG usage policy: %w", err)
	}
	if oldHelper.InboundTag != inboundTag || oldHelper.InterfaceName != state.InterfaceName || oldHelper.Transitioning || oldHelper.Stopped {
		return fmt.Errorf("AmneziaWG usage policy identity or lifecycle state is invalid")
	}
	oldConfigRaw, err := os.ReadFile(state.ConfigPath)
	if err != nil {
		return fmt.Errorf("read AmneziaWG runtime config for rollback: %w", err)
	}

	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.amneziaWGOfflineLocked(operationCtx, false, true); err != nil {
		return fmt.Errorf("checkpoint AmneziaWG usage before peer mutation: %w", err)
	}
	snapshot, err := amneziaWGPeerSnapshot(state.InterfaceName)
	if err != nil {
		return fmt.Errorf("capture AmneziaWG peer snapshot: %w", err)
	}
	if err := wireGuardPeerSnapshotMatchesAmneziaPolicy(snapshot, oldLive.Peers); err != nil {
		return fmt.Errorf("refuse stale AmneziaWG runtime snapshot: %w", err)
	}
	interfaceIdentity, err := amneziaWGInterfaceIdentity(state.InterfaceName)
	if err != nil {
		return fmt.Errorf("read AmneziaWG interface identity: %w", err)
	}
	targetDesired := make([]amneziaWGRuntimePeer, 0, len(desired))
	for _, peer := range newLive.Peers {
		if peer.UserID == userID {
			targetDesired = append(targetDesired, peer)
		}
	}
	delta, err := planAmneziaWGUserPeerDelta(oldLive.Peers, userID, operation, targetDesired)
	if err != nil {
		return err
	}
	kernelPlan, err := amneziaWGKernelPlan(delta, snapshot, userID)
	if err != nil {
		return err
	}
	if len(kernelPlan.Forward) == 0 && updatedNative == policy.NativeJSON {
		return nil
	}
	finalLive := oldLive
	finalLive.Peers = amneziaWGPeersFromWireGuard(delta.Peers)
	prepared, err := s.prepareAmneziaWGMutation(finalLive)
	if err != nil {
		return fmt.Errorf("validate updated AmneziaWG runtime config: %w", err)
	}
	prepared.ConfigPath = state.ConfigPath
	prepared.InterfaceName = state.InterfaceName
	targetPeers := make([]amneziaWGRuntimePeer, 0, len(finalLive.Peers))
	for _, peer := range finalLive.Peers {
		if peer.UserID == userID {
			targetPeers = append(targetPeers, peer)
		}
	}
	newHelper, err := amneziaWGMutationUsageConfig(oldHelper, userID, targetPeers)
	if err != nil {
		return err
	}
	transition := oldHelper
	transition.Transitioning = true
	transitionRaw, err := json.Marshal(transition)
	if err != nil {
		return err
	}
	if err := amneziaWGMutationDurableWrite(helperPath, transitionRaw); err != nil {
		return fmt.Errorf("persist AmneziaWG mutation fence: %w", err)
	}
	restoreOldHelper := func(cause error) error {
		return errors.Join(cause, amneziaWGMutationDurableWrite(helperPath, oldHelperRaw))
	}
	if err := executeAmneziaWGPeerMutationPlan(operationCtx, state.InterfaceName, kernelPlan); err != nil {
		return restoreOldHelper(err)
	}
	identityAfter, identityErr := amneziaWGInterfaceIdentity(state.InterfaceName)
	if identityErr != nil || identityAfter != interfaceIdentity {
		if identityErr == nil {
			identityErr = fmt.Errorf("interface identity changed during peer mutation")
		}
		return s.rollbackAmneziaWGMutation(kernelPlan, state, oldConfigRaw, helperPath, oldHelperRaw, false, fmt.Errorf("AmneziaWG interface preservation check failed: %w", identityErr))
	}
	if err := amneziaWGMutationWrite(state.ConfigPath, []byte(prepared.ConfigText), 0600); err != nil {
		return s.rollbackAmneziaWGMutation(kernelPlan, state, oldConfigRaw, helperPath, oldHelperRaw, false, fmt.Errorf("persist AmneziaWG runtime config: %w", err))
	}
	newHelperRaw, err := json.Marshal(newHelper)
	if err != nil {
		return s.rollbackAmneziaWGMutation(kernelPlan, state, oldConfigRaw, helperPath, oldHelperRaw, true, err)
	}
	if err := amneziaWGMutationDurableWrite(helperPath, newHelperRaw); err != nil {
		return s.rollbackAmneziaWGMutation(kernelPlan, state, oldConfigRaw, helperPath, oldHelperRaw, true, fmt.Errorf("persist AmneziaWG usage policy: %w", err))
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: policy.ConfigJSON, OvRuntimeJson: updatedNative, DesiredRevision: policy.Revision}); err != nil {
		return s.rollbackAmneziaWGMutation(kernelPlan, state, oldConfigRaw, helperPath, oldHelperRaw, true, fmt.Errorf("persist AmneziaWG native policy: %w", err))
	}
	if len(delta.Remove) > 0 {
		events := make([]nativeSessionEvent, 0, len(delta.Remove))
		for _, peer := range delta.Remove {
			kernelPeer := snapshot[strings.TrimSpace(peer.PublicKey)]
			events = append(events, amneziaWGSessionEvent(oldHelper, wireGuardPeerCounters{PublicKey: kernelPeer.PublicKey, Endpoint: kernelPeer.Endpoint}, userID, "stop"))
		}
		s.dispatchNativeSessionEvents(oldHelper.Callback, events, nil)
	}
	return nil
}

func (s *Server) rollbackAmneziaWGMutation(plan wireGuardPeerMutationPlan, state amneziaWGRuntimeState, oldConfigRaw []byte, helperPath string, oldHelperRaw []byte, configChanged bool, cause error) error {
	var restoreErr error
	if configChanged {
		restoreErr = errors.Join(restoreErr, amneziaWGMutationWrite(state.ConfigPath, oldConfigRaw, 0600))
	}
	restoreErr = errors.Join(restoreErr, amneziaWGMutationDurableWrite(helperPath, oldHelperRaw))
	inverse := wireGuardPeerMutationPlan{Forward: plan.Rollback, Rollback: plan.Forward}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(cause, restoreErr, executeAmneziaWGPeerMutationPlan(rollbackCtx, state.InterfaceName, inverse))
}

func findAmneziaWGInbound(inbounds []amneziaWGRuntimeInbound, tag string) (amneziaWGRuntimeInbound, bool) {
	var result amneziaWGRuntimeInbound
	found := false
	for _, inbound := range inbounds {
		if strings.TrimSpace(inbound.Tag) != strings.TrimSpace(tag) {
			continue
		}
		if found {
			return amneziaWGRuntimeInbound{}, false
		}
		result, found = inbound, true
	}
	return result, found
}

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
			ReflectedUsageBatchID: peer.ReflectedUsageBatchID, ReflectedUsageLedger: peer.ReflectedUsageLedger, UserID: peer.UserID,
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
