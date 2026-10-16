package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const (
	l2TPMutationUserIDField = "l2tp_user_id"
	l2TPMutationOperation   = "l2tp_operation"
	l2TPMutationRevision    = "l2tp_revision"
	l2TPMutationUserJSON    = "l2tp_user_json"
	l2TPRemoveUserPrefix    = "l2tp-user:"
)

var l2TPMutationCheckpoint = (*Server).checkpointPPPOffline
var l2TPMutationTerminateSession = pppOfflineTerminateSession
var l2TPMutationRuntimeActive = l2TPSystemServiceActive

func decodeL2TPMutationUser(user *nodev1.InboundUser, envelopeOperation string) (int64, uint64, string, *l2TPRuntimeUser, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "l2tp") {
		return 0, 0, "", nil, fmt.Errorf("L2TP mutation protocol is required")
	}
	fields := user.GetFields()
	userID, err := strconv.ParseInt(strings.TrimSpace(fields[l2TPMutationUserIDField]), 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, "", nil, fmt.Errorf("L2TP user id is invalid")
	}
	revision, err := strconv.ParseUint(strings.TrimSpace(fields[l2TPMutationRevision]), 10, 64)
	if err != nil || revision == 0 {
		return 0, 0, "", nil, fmt.Errorf("L2TP desired revision is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[l2TPMutationOperation]))
	if operation != strings.ToLower(strings.TrimSpace(envelopeOperation)) {
		return 0, 0, "", nil, fmt.Errorf("L2TP operation envelope mismatch")
	}
	var desired *l2TPRuntimeUser
	if operation == "remove" {
		if strings.TrimSpace(fields[l2TPMutationUserJSON]) != "" {
			return 0, 0, "", nil, fmt.Errorf("L2TP remove mutation cannot include desired user")
		}
	} else {
		var parsed l2TPRuntimeUser
		if err := json.Unmarshal([]byte(fields[l2TPMutationUserJSON]), &parsed); err != nil {
			return 0, 0, "", nil, fmt.Errorf("decode L2TP desired user: %w", err)
		}
		if parsed.UserID != userID {
			return 0, 0, "", nil, fmt.Errorf("L2TP desired user id does not match mutation")
		}
		desired = &parsed
	}
	return userID, revision, operation, desired, nil
}

func l2TPRemoveUserIdentity(value string) (int64, uint64, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), l2TPRemoveUserPrefix), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	userID, idErr := strconv.ParseInt(parts[0], 10, 64)
	revision, revisionErr := strconv.ParseUint(parts[1], 10, 64)
	return userID, revision, idErr == nil && userID > 0 && revisionErr == nil && revision > 0
}

func l2TPMutationDigest(inboundTag string, userID int64, operation string, desired *l2TPRuntimeUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string           `json:"inbound_tag"`
		UserID     int64            `json:"user_id"`
		Operation  string           `json:"operation"`
		Desired    *l2TPRuntimeUser `json:"desired,omitempty"`
	}{strings.TrimSpace(inboundTag), userID, operation, desired})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) l2TPMutationReceiptPath(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "l2tp", "mutation-results", hex.EncodeToString(sum[:])+".json")
}

func (s *Server) l2TPMutationAlreadyCompleted(operationID, digest string) (bool, error) {
	raw, err := os.ReadFile(s.l2TPMutationReceiptPath(operationID))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read L2TP operation receipt: %w", err)
	}
	var receipt openVPNMutationResult
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
		return false, fmt.Errorf("L2TP operation receipt is corrupt")
	}
	if receipt.Digest != digest {
		return false, fmt.Errorf("L2TP operation id was reused with a different mutation")
	}
	return true, nil
}

func (s *Server) recordL2TPMutationCompleted(operationID, digest string) error {
	path := s.l2TPMutationReceiptPath(operationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create L2TP operation receipt directory: %w", err)
	}
	raw, err := json.Marshal(openVPNMutationResult{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("persist L2TP operation receipt: %w", err)
	}
	return nil
}

func (s *Server) applyL2TPUserMutationOperation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, revision, encodedOperation, desired, err := decodeL2TPMutationUser(user, operation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("L2TP mutation operation id is required")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return fmt.Errorf("L2TP inbound tag is required")
	}
	unlock, err := s.lockOpenVPNMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()
	digest, err := l2TPMutationDigest(inboundTag, userID, encodedOperation, desired)
	if err != nil {
		return err
	}
	completed, err := s.l2TPMutationAlreadyCompleted(operationID, digest)
	if err != nil || completed {
		if err != nil {
			return err
		}
		return s.advanceL2TPMutationRevision(revision)
	}
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load L2TP runtime policy: %w", err)
	}
	if err := validateRuntimePolicyRevision(policy.Revision, revision); err != nil {
		return fmt.Errorf("stale L2TP mutation revision: %w", err)
	}
	oldUser, err := l2TPPolicyUser(policy.NativeJSON, inboundTag, userID)
	if err != nil {
		return err
	}
	revokeTarget := l2TPMutationNeedsTargetDisconnect(encodedOperation, desired, oldUser)
	updatedNative, changed, err := planL2TPUserPolicyMutation(policy.NativeJSON, inboundTag, userID, encodedOperation, desired)
	if err != nil {
		return err
	}
	if !changed {
		if revokeTarget {
			live, liveErr := s.l2TPUserHasLiveSessions(inboundTag, userID)
			if liveErr != nil {
				return fmt.Errorf("verify L2TP target session state: %w", liveErr)
			}
			if live {
				return fmt.Errorf("L2TP policy is already applied but user %d still has a live or pending PPP session", userID)
			}
		}
		if err := s.advanceL2TPMutationRevision(revision); err != nil {
			return err
		}
		return s.recordL2TPMutationCompleted(operationID, digest)
	}
	updatedPayload, err := parseNativeRuntimePayload(updatedNative)
	if err != nil {
		return err
	}
	var inbound *l2TPRuntimeInbound
	for i := range updatedPayload.L2TPInbounds {
		if strings.TrimSpace(updatedPayload.L2TPInbounds[i].Tag) == inboundTag {
			inbound = &updatedPayload.L2TPInbounds[i]
			break
		}
	}
	if inbound == nil {
		return fmt.Errorf("L2TP inbound %q is absent from updated policy", inboundTag)
	}
	s.mu.Lock()
	runtime := s.l2TPRuntimes[inboundTag]
	active := runtime != nil
	s.mu.Unlock()
	if !active || !l2TPMutationRuntimeActive() {
		return fmt.Errorf("L2TP inbound %q has no live managed runtime", inboundTag)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = l2TPMutationCheckpoint(s, checkpointCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("checkpoint PPP accounting before L2TP user mutation: %w", err)
	}
	files, transitioning, err := prepareL2TPMutationFiles(s.cfg.DataDir, *inbound, updatedPayload.SessionCallback)
	if err != nil {
		return err
	}
	transitionPath := filepath.Join(s.cfg.DataDir, "l2tp", l2TPRuntimeDirName(inboundTag), "session-helper.json")
	helperExecutable, err := pppSessionHelperExecutable()
	if err != nil {
		return fmt.Errorf("resolve PPP session helper for L2TP mutation: %w", err)
	}
	helperExecutable, err = filepath.Abs(helperExecutable)
	if err != nil {
		return fmt.Errorf("resolve absolute PPP session helper for L2TP mutation: %w", err)
	}
	runtimeRoot := filepath.Dir(transitionPath)
	identityFiles := l2TPRuntimeFiles{
		IPSecConfig: filepath.Join(runtimeRoot, "ipsec.conf"), IPSecSecrets: filepath.Join(runtimeRoot, "ipsec.secrets"),
		XL2TPConfig: filepath.Join(runtimeRoot, "xl2tpd.conf"), PPPOptions: filepath.Join(runtimeRoot, "ppp-options"),
		CHAPSecrets: filepath.Join(runtimeRoot, "chap-secrets"), IPUpScript: filepath.Join(runtimeRoot, "ip-up.sh"),
		IPDownScript: filepath.Join(runtimeRoot, "ip-down.sh"), SessionConfig: transitionPath,
		UsageConfig: filepath.Join(runtimeRoot, "usage-helper.json"),
	}
	identityOverrides := map[string][]byte{}
	for _, path := range []string{identityFiles.CHAPSecrets, identityFiles.SessionConfig, identityFiles.UsageConfig} {
		staged, ok := files[path]
		if !ok || staged.remove {
			return fmt.Errorf("L2TP mutation is missing a required staged policy file")
		}
		identityOverrides[path] = staged.data
	}
	runtimeIdentity, err := l2TPRuntimeIdentityWithOverrides(identityFiles, helperExecutable, identityOverrides)
	if err != nil {
		return fmt.Errorf("compute target L2TP runtime identity: %w", err)
	}
	transitioning.Transitioning = true
	transitionRaw, err := json.Marshal(transitioning)
	if err != nil {
		return err
	}
	var transitionBackup []openVPNMutationFileBackup
	err = withPPPAdmissionLock(s.cfg.DataDir, func() error {
		var applyErr error
		transitionBackup, applyErr = applyOpenVPNMutationFiles(map[string]openVPNMutationFile{
			transitionPath: {data: transitionRaw},
		})
		return applyErr
	})
	if err != nil {
		return err
	}
	otherFiles := make(map[string]openVPNMutationFile, len(files))
	for path, file := range files {
		if path != transitionPath {
			otherFiles[path] = file
		}
	}
	fileBackups, err := applyOpenVPNMutationFiles(otherFiles)
	allBackups := append(transitionBackup, fileBackups...)
	if err != nil {
		return errorsJoinRollback(err, transitionBackup)
	}
	rollbackFiles := func(cause error) error { return errorsJoinRollback(cause, allBackups) }
	if err := ctx.Err(); err != nil {
		return rollbackFiles(err)
	}
	if err := s.commitL2TPMutationPolicy(policy.Checksum, policy.ConfigJSON, updatedNative, revision); err != nil {
		return rollbackFiles(fmt.Errorf("persist L2TP user policy: %w", err))
	}
	if err := applyOpenVPNMutationFilesSingle(transitionPath, openVPNMutationFile{data: files[transitionPath].data}); err != nil {
		rollbackPolicyErr := s.restoreL2TPMutationPolicy(policy, updatedNative, revision)
		rollbackFilesErr := restoreOpenVPNMutationFiles(allBackups)
		return errors.Join(fmt.Errorf("activate L2TP user policy: %w", err), rollbackPolicyErr, rollbackFilesErr)
	}
	s.setL2TPRuntimeIdentity(inboundTag, runtimeIdentity)
	s.wakeLocalQuota("l2tp")
	if revokeTarget {
		if err := s.terminateL2TPUserSessions(ctx, inboundTag, userID); err != nil {
			return fmt.Errorf("L2TP policy applied but targeted session revoke failed for user %d: %w", userID, err)
		}
	}
	if err := s.recordL2TPMutationCompleted(operationID, digest); err != nil {
		return err
	}
	s.mu.Lock()
	if revision > s.appliedRev {
		s.appliedRev = revision
	}
	s.mu.Unlock()
	s.appendLog(fmt.Sprintf("L2TP user mutation committed operation=%s inbound=%s user_id=%d action=%s", operationID, inboundTag, userID, encodedOperation))
	return nil
}

func l2TPMutationNeedsTargetDisconnect(operation string, desired, previous *l2TPRuntimeUser) bool {
	if operation == "remove" || (desired != nil && !l2TPUserAllowed(*desired)) {
		return true
	}
	return operation == "update" && desired != nil && previous != nil && strings.TrimSpace(desired.IPv4Address) != strings.TrimSpace(previous.IPv4Address)
}

func l2TPPolicyUser(nativeJSON, inboundTag string, userID int64) (*l2TPRuntimeUser, error) {
	payload, err := parseNativeRuntimePayload(nativeJSON)
	if err != nil {
		return nil, err
	}
	for _, inbound := range payload.L2TPInbounds {
		if strings.TrimSpace(inbound.Tag) != strings.TrimSpace(inboundTag) {
			continue
		}
		for i := range inbound.Users {
			if inbound.Users[i].UserID == userID {
				user := inbound.Users[i]
				return &user, nil
			}
		}
	}
	return nil, nil
}

func prepareL2TPMutationFiles(dataDir string, inbound l2TPRuntimeInbound, callback nativeRuntimeSessionCallback) (map[string]openVPNMutationFile, nativeSessionHelperConfig, error) {
	root := filepath.Join(dataDir, "l2tp", l2TPRuntimeDirName(inbound.Tag))
	paths := l2TPRuntimeFiles{
		CHAPSecrets:   filepath.Join(root, "chap-secrets"),
		SessionConfig: filepath.Join(root, "session-helper.json"),
		UsageConfig:   filepath.Join(root, "usage-helper.json"),
	}
	rawCurrent, err := os.ReadFile(paths.SessionConfig)
	if err != nil {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("read current L2TP session policy: %w", err)
	}
	var current nativeSessionHelperConfig
	if err := json.Unmarshal(rawCurrent, &current); err != nil {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("decode current L2TP session policy: %w", err)
	}
	if current.InboundTag != strings.TrimSpace(inbound.Tag) || current.Protocol != "l2tp" {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("current L2TP session policy identity mismatch")
	}
	stateDir := current.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(root, "sessions")
	}
	users := make(map[string]int64, len(inbound.Users))
	for _, user := range inbound.Users {
		username := l2TPRuntimeUsername(user)
		if username == "" || user.UserID <= 0 {
			return nil, nativeSessionHelperConfig{}, fmt.Errorf("L2TP user %d has invalid session identity", user.UserID)
		}
		users[username] = user.UserID
	}
	session := nativeSessionHelperConfig{
		Callback: callback, InboundTag: strings.TrimSpace(inbound.Tag), Protocol: "l2tp",
		Users: users, Policies: buildNativeSessionUserPolicies(l2TPUsersAsOpenVPNUsers(inbound.Users)), StateDir: stateDir,
	}
	sessionRaw, err := json.Marshal(session)
	if err != nil {
		return nil, nativeSessionHelperConfig{}, err
	}
	usageRaw, err := json.Marshal(l2TPUsageConfigForInbound(inbound))
	if err != nil {
		return nil, nativeSessionHelperConfig{}, err
	}
	globalCHAP, err := os.ReadFile(l2TPCHAPSecretsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nativeSessionHelperConfig{}, err
	}
	globalCHAP = replaceL2TPManagedBlock(globalCHAP, l2TPCHAPBlockStart, l2TPCHAPBlockEnd, renderL2TPCHAPSecrets(inbound.Users))
	files := map[string]openVPNMutationFile{
		paths.CHAPSecrets:   {data: []byte(renderL2TPCHAPSecrets(inbound.Users))},
		paths.SessionConfig: {data: sessionRaw},
		paths.UsageConfig:   {data: usageRaw},
		l2TPCHAPSecretsPath: {data: globalCHAP},
	}
	return files, current, nil
}

func applyOpenVPNMutationFilesSingle(path string, file openVPNMutationFile) error {
	_, err := applyOpenVPNMutationFiles(map[string]openVPNMutationFile{path: file})
	return err
}

func errorsJoinRollback(cause error, backups []openVPNMutationFileBackup) error {
	if len(backups) == 0 {
		return cause
	}
	return errors.Join(cause, restoreOpenVPNMutationFiles(backups))
}

func (s *Server) commitL2TPMutationPolicy(expectedChecksum, configJSON, nativeJSON string, revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy disappeared before L2TP mutation commit")
		}
		return err
	}
	if current.Checksum != expectedChecksum {
		return fmt.Errorf("L2TP mutation snapshot changed before commit")
	}
	if revision < current.Revision {
		return fmt.Errorf("stale L2TP mutation revision %d; current revision is %d", revision, current.Revision)
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{ConfigJson: configJSON, OvRuntimeJson: nativeJSON, DesiredRevision: revision})
}

func (s *Server) advanceL2TPMutationRevision(revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy is unavailable during L2TP operation replay")
		}
		return err
	}
	if err := validateRuntimePolicyRevision(current.Revision, revision); err != nil {
		return err
	}
	if revision <= current.Revision {
		return nil
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{ConfigJson: current.ConfigJSON, OvRuntimeJson: current.NativeJSON, DesiredRevision: revision})
}

func (s *Server) restoreL2TPMutationPolicy(previous *runtimePolicySnapshot, updatedNative string, revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		return err
	}
	if current.NativeJSON != updatedNative || current.Revision != revision {
		return fmt.Errorf("refuse L2TP policy rollback after a newer mutation")
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{ConfigJson: previous.ConfigJSON, OvRuntimeJson: previous.NativeJSON, DesiredRevision: previous.Revision})
}

func (s *Server) setL2TPRuntimeIdentity(tag string, identity [32]byte) {
	s.mu.Lock()
	if runtime := s.l2TPRuntimes[tag]; runtime != nil {
		runtime.identity = identity
	}
	s.mu.Unlock()
}

func (s *Server) l2TPUserHasLiveSessions(inboundTag string, userID int64) (bool, error) {
	runtimeRoot := filepath.Join(s.cfg.DataDir, "l2tp", l2TPRuntimeDirName(inboundTag))
	activePaths, err := filepath.Glob(filepath.Join(runtimeRoot, "ppp-accounting", "active", "ppp[0-9]*-*.json"))
	if err != nil {
		return false, err
	}
	for _, path := range activePaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		var session pppOfflineSession
		if err := json.Unmarshal(raw, &session); err != nil {
			return false, fmt.Errorf("decode active L2TP session: %w", err)
		}
		if session.InboundTag == inboundTag && session.UserID == userID && pppOfflineProcessIsLive(session.Process) {
			return true, nil
		}
	}
	pendingPaths, err := filepath.Glob(filepath.Join(s.cfg.DataDir, "ppp-admission", "pending", "*.json"))
	if err != nil {
		return false, err
	}
	for _, path := range pendingPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		var reservation pppOfflineSession
		if err := json.Unmarshal(raw, &reservation); err != nil {
			return false, fmt.Errorf("decode pending PPP admission: %w", err)
		}
		if reservation.InboundTag == inboundTag && reservation.UserID == userID && pppOfflineProcessIsLive(reservation.Process) {
			return true, nil
		}
	}
	return false, nil
}

func pppOfflineProcessIsLive(process string) bool {
	parts := strings.Split(process, ":")
	if len(parts) != 3 {
		return true // Unknown identity must block a successful remove acknowledgement.
	}
	identity, err := pppOfflineReadProcess(parts[1])
	if os.IsNotExist(err) {
		return false
	}
	return err != nil || identity == process
}

func (s *Server) terminateL2TPUserSessions(ctx context.Context, inboundTag string, userID int64) error {
	root := filepath.Join(s.cfg.DataDir, "l2tp", l2TPRuntimeDirName(inboundTag))
	paths, err := filepath.Glob(filepath.Join(root, "ppp-accounting", "active", "ppp[0-9]*-*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var session pppOfflineSession
		if err := json.Unmarshal(raw, &session); err != nil {
			return fmt.Errorf("decode L2TP active session: %w", err)
		}
		if session.InboundTag != inboundTag || session.UserID != userID {
			continue
		}
		if !pppOfflineProcessIsLive(session.Process) {
			// Stop callbacks can be delayed or retried after the process exits.
			// Retained accounting metadata is not an active session and must not
			// make a targeted remove fail.
			continue
		}
		if err := l2TPMutationTerminateSession(ctx, session.Process); err != nil {
			// The process can exit after the liveness check but before pidfd_open.
			// Only suppress that race after revalidating this exact process
			// generation as gone; permission and other inspection errors remain
			// conservative failures in pppOfflineProcessIsLive.
			if !pppOfflineProcessIsLive(session.Process) {
				continue
			}
			return fmt.Errorf("terminate target PPP process: %w", err)
		}
	}
	return nil
}
