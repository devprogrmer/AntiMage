package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// applyWireGuardUserMutation changes one inbound while holding the same
// accounting lock used by quota enforcement. Its caller must already hold the
// per-inbound runtime lifecycle mutation gate.
func (s *Server) applyWireGuardUserMutation(ctx context.Context, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, encodedOperation, desired, err := decodeWireGuardMutationUser(user)
	if err != nil {
		return err
	}
	if encodedOperation != strings.ToLower(strings.TrimSpace(operation)) {
		return fmt.Errorf("WireGuard operation envelope mismatch")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return fmt.Errorf("WireGuard inbound tag is required")
	}
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load WireGuard runtime policy: %w", err)
	}

	updatedNative, delta, err := planWireGuardNativePolicyMutation(policy.NativeJSON, inboundTag, userID, operation, desired)
	if err != nil {
		return err
	}
	payload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil {
		return err
	}
	inboundIndex := -1
	for i := range payload.WireGuardInbounds {
		if strings.TrimSpace(payload.WireGuardInbounds[i].Tag) == inboundTag {
			inboundIndex = i
			break
		}
	}
	if inboundIndex < 0 {
		return fmt.Errorf("WireGuard inbound %q disappeared from committed policy", inboundTag)
	}
	inbound := payload.WireGuardInbounds[inboundIndex]
	configRoot := filepath.Join(s.cfg.DataDir, "wireguard", "runtime", wireGuardRuntimeDirName(inboundTag))
	expectedConfigPath := filepath.Join(configRoot, "wg.conf")
	pool, _, err := wireGuardRuntimeAddressing(inbound.Settings)
	if err != nil {
		return fmt.Errorf("validate WireGuard address pool: %w", err)
	}
	globalPSK := wireGuardStringSetting(inbound.Settings, "preshared_key")
	if err := validateWireGuardKey(globalPSK, "preshared_key", true); err != nil {
		return err
	}
	for _, peer := range desired {
		if err := validateWireGuardKey(peer.PublicKey, "peer public_key", false); err != nil {
			return err
		}
		if err := validateWireGuardKey(peer.PresharedKey, "peer preshared_key", true); err != nil {
			return err
		}
		address, addressErr := netip.ParseAddr(strings.TrimSpace(peer.Address))
		if addressErr != nil || !wireGuardUsableAddress(pool, address) {
			return fmt.Errorf("WireGuard peer %q address is outside the usable inbound pool", peer.PublicKey)
		}
		if allowed, reason := nativeSessionUserPolicyAllowed(wireGuardRuntimePeerPolicy(peer), time.Now().UTC()); !allowed {
			return fmt.Errorf("WireGuard user policy denies requested peer %q: %s", peer.PublicKey, reason)
		}
	}
	s.mu.Lock()
	for _, peer := range delta.Set {
		if _, suppressed := s.wireGuardDynamicSuppressedPeers[wireGuardDynamicSuppressionKey(inboundTag, peer.PublicKey)]; suppressed {
			s.mu.Unlock()
			return fmt.Errorf("WireGuard peer %q is dynamically suppressed until policy removal is synchronized", peer.PublicKey)
		}
	}
	s.mu.Unlock()

	state, ok, err := s.wireGuardMutationRuntimeState(inboundTag)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("WireGuard inbound %q has no managed runtime", inboundTag)
	}
	if state.InterfaceName == "" || state.ConfigPath == "" {
		return fmt.Errorf("WireGuard inbound %q has incomplete runtime state", inboundTag)
	}
	if !filepath.IsAbs(state.ConfigPath) {
		return fmt.Errorf("WireGuard inbound %q has an invalid config path", inboundTag)
	}
	if filepath.Clean(state.ConfigPath) != filepath.Clean(expectedConfigPath) {
		return fmt.Errorf("WireGuard runtime config path does not match managed inbound")
	}
	owned, err := s.wireGuardInterfaceOwnedByAntiMage(inboundTag, state.InterfaceName)
	if err != nil {
		return fmt.Errorf("verify WireGuard interface ownership: %w", err)
	}
	if !owned {
		return fmt.Errorf("WireGuard interface %q is not managed by AntiMage", state.InterfaceName)
	}

	helperPath := filepath.Join(s.cfg.DataDir, "wireguard", "inbounds", wireGuardRuntimeDirName(inboundTag), "usage-helper.json")
	oldHelperRaw, err := os.ReadFile(helperPath)
	if err != nil {
		return fmt.Errorf("read WireGuard usage policy: %w", err)
	}
	var oldHelper wireGuardUsageRuntimeConfig
	if err := json.Unmarshal(oldHelperRaw, &oldHelper); err != nil {
		return fmt.Errorf("decode WireGuard usage policy: %w", err)
	}
	if oldHelper.InboundTag != inboundTag || oldHelper.InterfaceName != state.InterfaceName {
		return fmt.Errorf("WireGuard usage policy identity does not match runtime state")
	}
	oldConfigRaw, err := os.ReadFile(state.ConfigPath)
	if err != nil {
		return fmt.Errorf("read WireGuard runtime config for rollback: %w", err)
	}

	wgPath, err := wireGuardRuntimeLookPath("wg")
	if err != nil {
		return fmt.Errorf("WireGuard command is unavailable: %w", err)
	}
	ipPath, err := wireGuardRuntimeLookPath("ip")
	if err != nil {
		return fmt.Errorf("ip command is unavailable: %w", err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	interfaceExists, err := wireGuardInterfaceExists(operationCtx, ipPath, state.InterfaceName)
	if err != nil || !interfaceExists {
		if err == nil {
			err = fmt.Errorf("interface is not present")
		}
		return fmt.Errorf("WireGuard interface %q is unavailable: %w", state.InterfaceName, err)
	}
	interfaceIdentity, err := wireGuardInterfaceIdentity(state.InterfaceName)
	if err != nil {
		return fmt.Errorf("read WireGuard interface identity: %w", err)
	}

	s.wireGuardUsageMu.Lock()
	defer s.wireGuardUsageMu.Unlock()
	if err := s.checkWireGuardOfflineGenerationLocked(operationCtx, false, state.InterfaceName); err != nil {
		return fmt.Errorf("checkpoint WireGuard usage before peer mutation: %w", err)
	}

	rawDump, err := wireGuardDumpInterface(operationCtx, state.InterfaceName)
	if err != nil {
		return fmt.Errorf("capture WireGuard peer snapshot: %w", err)
	}
	snapshot, err := parseWireGuardPeerSnapshot(string(rawDump))
	if err != nil {
		return fmt.Errorf("parse WireGuard peer snapshot: %w", err)
	}
	peerListRaw, err := wireGuardRuntimeRun(operationCtx, wgPath, "show", state.InterfaceName, "peers")
	if err != nil {
		return fmt.Errorf("verify WireGuard peer inventory: %w", err)
	}
	listed, err := parseWireGuardPeerList(string(peerListRaw))
	if err != nil || !wireGuardPeerInventoriesMatch(snapshot, listed) {
		if err == nil {
			err = fmt.Errorf("peer list differs from complete dump")
		}
		return fmt.Errorf("refuse stale or incomplete WireGuard snapshot: %w", err)
	}
	filtered, _ := filterWireGuardRuntimeInboundByStaticPolicy(inbound)
	filtered = s.filterWireGuardRuntimeInboundByDynamicSuppression(filtered)
	expected := make(map[string]struct{}, len(filtered.Peers))
	for _, peer := range filtered.Peers {
		expected[strings.TrimSpace(peer.PublicKey)] = struct{}{}
	}
	if !wireGuardPeerInventoriesMatch(snapshot, expected) {
		return fmt.Errorf("refuse WireGuard mutation: runtime peers differ from committed policy")
	}
	if err := wireGuardPeerSnapshotMatchesPolicy(snapshot, filtered.Peers, globalPSK); err != nil {
		return fmt.Errorf("refuse WireGuard mutation with stale peer configuration: %w", err)
	}

	privateKey := wireGuardStringSetting(inbound.Settings, "private_key")
	updatedInbound := inbound
	updatedInbound.Peers = delta.Peers
	updatedConfig, err := renderWireGuardRuntimeConfig(updatedInbound, privateKey, globalPSK)
	if err != nil {
		return fmt.Errorf("render updated WireGuard runtime config: %w", err)
	}
	kernelPlan, err := planWireGuardKernelMutation(delta, snapshot, userID, globalPSK)
	if err != nil {
		return err
	}
	if len(kernelPlan.Forward) == 0 && updatedNative == policy.NativeJSON {
		return nil
	}
	removing := make([]wireGuardRuntimePeer, 0, len(delta.Remove))
	reconcileGenerations := func() error {
		reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer reconcileCancel()
		var generationErr error
		for _, peer := range removing {
			generationErr = errors.Join(generationErr, s.transitionWireGuardPeerGenerationLocked(inboundTag, state.InterfaceName, peer.PublicKey, false))
		}
		generationErr = errors.Join(generationErr, s.checkWireGuardOfflineGenerationLocked(reconcileCtx, false, state.InterfaceName))
		return generationErr
	}
	for _, peer := range delta.Remove {
		if err := s.transitionWireGuardPeerGenerationLocked(inboundTag, state.InterfaceName, peer.PublicKey, true); err != nil {
			return errors.Join(fmt.Errorf("mark WireGuard peer generation transition: %w", err), reconcileGenerations())
		}
		removing = append(removing, peer)
	}
	if err := executeWireGuardPeerMutationPlan(operationCtx, wgPath, state.InterfaceName, kernelPlan); err != nil {
		return errors.Join(err, reconcileGenerations())
	}
	rollbackKernel := func(cause error) error {
		inverse := wireGuardPeerMutationPlan{Forward: kernelPlan.Rollback, Rollback: kernelPlan.Forward}
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rollbackCancel()
		rollbackErr := executeWireGuardPeerMutationPlan(rollbackCtx, wgPath, state.InterfaceName, inverse)
		return errors.Join(cause, rollbackErr, reconcileGenerations())
	}
	for _, peer := range removing {
		if err := s.transitionWireGuardPeerGenerationLocked(inboundTag, state.InterfaceName, peer.PublicKey, false); err != nil {
			return rollbackKernel(fmt.Errorf("finalize WireGuard peer generation transition: %w", err))
		}
	}
	identityAfterKernel, err := wireGuardInterfaceIdentity(state.InterfaceName)
	if err != nil || identityAfterKernel != interfaceIdentity {
		if err == nil {
			err = fmt.Errorf("interface identity changed during peer mutation")
		}
		return rollbackKernel(fmt.Errorf("WireGuard interface preservation check failed: %w", err))
	}
	if err := wireGuardMutationConfigWrite(state.ConfigPath, []byte(updatedConfig), 0600); err != nil {
		return rollbackKernel(fmt.Errorf("persist WireGuard runtime config: %w", err))
	}
	restoreConfig := func(cause error) error {
		restoreErr := wireGuardMutationConfigWrite(state.ConfigPath, oldConfigRaw, 0600)
		return rollbackKernel(errors.Join(cause, restoreErr))
	}

	newHelper := oldHelper
	newHelper.Peers = make(map[string]int64, len(delta.Peers))
	newHelper.PeerAddresses = make(map[string]string, len(delta.Peers))
	newHelper.Policies = make(map[string]nativeSessionUserPolicy, len(delta.Peers))
	for _, peer := range delta.Peers {
		key := strings.TrimSpace(peer.PublicKey)
		newHelper.Peers[key] = peer.UserID
		newHelper.PeerAddresses[key] = strings.TrimSpace(peer.Address)
		newHelper.Policies[key] = wireGuardRuntimePeerPolicy(peer)
	}
	if err := writeWireGuardUsageConfig(helperPath, newHelper); err != nil {
		return restoreConfig(fmt.Errorf("persist WireGuard usage policy: %w", err))
	}
	policyRequest := &nodev1.RuntimeConfigRequest{
		ConfigJson: policy.ConfigJSON, OvRuntimeJson: updatedNative, DesiredRevision: policy.Revision,
	}
	if err := s.persistRuntimePolicy(policyRequest); err != nil {
		restoreErr := writeWireGuardUsageConfig(helperPath, oldHelper)
		return restoreConfig(errors.Join(fmt.Errorf("persist WireGuard native policy: %w", err), restoreErr))
	}
	return nil
}

func (s *Server) wireGuardMutationRuntimeState(tag string) (wireGuardRuntimeState, bool, error) {
	s.mu.Lock()
	state, ok := s.wireGuardRuntimes[tag]
	s.mu.Unlock()
	if ok {
		return state, true, nil
	}
	states, err := s.loadWireGuardRuntimeStates()
	if err != nil {
		return wireGuardRuntimeState{}, false, err
	}
	state, ok = states[tag]
	return state, ok, nil
}

var wireGuardMutationConfigWrite = atomicWriteFile
