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
	pptpMutationUserIDField = "pptp_user_id"
	pptpMutationOperation   = "pptp_operation"
	pptpMutationRevision    = "pptp_revision"
	pptpMutationUserJSON    = "pptp_user_json"
	pptpRemoveUserPrefix    = "pptp-user:"
)

// pptpMutationCheckpoint, pptpMutationRuntimeActive and
// pptpMutationTerminateSession are seams for tests. The PPTP runtime is a single
// pptpd supervisor that spawns one pppd worker per session, so a user-only
// change never restarts the supervisor: we only stage the identity files pptpd
// and pppd read per new session.
var pptpMutationCheckpoint = (*Server).checkpointPPPOffline
var pptpMutationRuntimeActive = func() bool { return true }
var pptpMutationTerminateSession = pppOfflineTerminateSession

// pptpMutationCommitOverride lets tests force a persistence failure to exercise
// the staged-file rollback path. It is nil outside tests.
var pptpMutationCommitOverride func(*Server, string, string, string, uint64) error

func decodePPTPMutationUser(user *nodev1.InboundUser, envelopeOperation string) (int64, uint64, string, *pptpRuntimeUser, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "pptp") {
		return 0, 0, "", nil, fmt.Errorf("PPTP mutation protocol is required")
	}
	fields := user.GetFields()
	userID, err := strconv.ParseInt(strings.TrimSpace(fields[pptpMutationUserIDField]), 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, "", nil, fmt.Errorf("PPTP user id is invalid")
	}
	revision, err := strconv.ParseUint(strings.TrimSpace(fields[pptpMutationRevision]), 10, 64)
	if err != nil || revision == 0 {
		return 0, 0, "", nil, fmt.Errorf("PPTP desired revision is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[pptpMutationOperation]))
	if operation != strings.ToLower(strings.TrimSpace(envelopeOperation)) {
		return 0, 0, "", nil, fmt.Errorf("PPTP operation envelope mismatch")
	}
	var desired *pptpRuntimeUser
	if operation == "remove" {
		if strings.TrimSpace(fields[pptpMutationUserJSON]) != "" {
			return 0, 0, "", nil, fmt.Errorf("PPTP remove mutation cannot include desired user")
		}
	} else {
		var parsed pptpRuntimeUser
		if err := json.Unmarshal([]byte(fields[pptpMutationUserJSON]), &parsed); err != nil {
			return 0, 0, "", nil, fmt.Errorf("decode PPTP desired user: %w", err)
		}
		if parsed.UserID != userID {
			return 0, 0, "", nil, fmt.Errorf("PPTP desired user id does not match mutation")
		}
		desired = &parsed
	}
	return userID, revision, operation, desired, nil
}

func pptpRemoveUserIdentity(value string) (int64, uint64, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), pptpRemoveUserPrefix), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	userID, idErr := strconv.ParseInt(parts[0], 10, 64)
	revision, revisionErr := strconv.ParseUint(parts[1], 10, 64)
	return userID, revision, idErr == nil && userID > 0 && revisionErr == nil && revision > 0
}

func pptpMutationDigest(inboundTag string, userID int64, operation string, desired *pptpRuntimeUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string           `json:"inbound_tag"`
		UserID     int64            `json:"user_id"`
		Operation  string           `json:"operation"`
		Desired    *pptpRuntimeUser `json:"desired,omitempty"`
	}{strings.TrimSpace(inboundTag), userID, operation, desired})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) pptpMutationReceiptPath(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "pptp", "mutation-results", hex.EncodeToString(sum[:])+".json")
}

func (s *Server) pptpMutationAlreadyCompleted(operationID, digest string) (bool, error) {
	raw, err := os.ReadFile(s.pptpMutationReceiptPath(operationID))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read PPTP operation receipt: %w", err)
	}
	var receipt openVPNMutationResult
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
		return false, fmt.Errorf("PPTP operation receipt is corrupt")
	}
	if receipt.Digest != digest {
		return false, fmt.Errorf("PPTP operation id was reused with a different mutation")
	}
	return true, nil
}

func (s *Server) recordPPTPMutationCompleted(operationID, digest string) error {
	path := s.pptpMutationReceiptPath(operationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create PPTP operation receipt directory: %w", err)
	}
	raw, err := json.Marshal(openVPNMutationResult{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("persist PPTP operation receipt: %w", err)
	}
	return nil
}

func (s *Server) applyPPTPUserMutationOperation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, revision, encodedOperation, desired, err := decodePPTPMutationUser(user, operation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("PPTP mutation operation id is required")
	}
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return fmt.Errorf("PPTP mutation inbound tag is required")
	}
	unlock, err := s.lockOpenVPNMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()
	digest, err := pptpMutationDigest(inboundTag, userID, encodedOperation, desired)
	if err != nil {
		return err
	}
	completed, err := s.pptpMutationAlreadyCompleted(operationID, digest)
	if err != nil || completed {
		if err != nil {
			return err
		}
		return s.advancePPTPMutationRevision(revision)
	}
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load PPTP runtime policy: %w", err)
	}
	if err := validateRuntimePolicyRevision(policy.Revision, revision); err != nil {
		return fmt.Errorf("stale PPTP mutation revision: %w", err)
	}
	oldUser, err := pptpPolicyUser(policy.NativeJSON, inboundTag, userID)
	if err != nil {
		return err
	}
	revokeTarget := pptpMutationNeedsTargetDisconnect(encodedOperation, desired, oldUser)
	updatedNative, changed, err := planPPTPUserPolicyMutation(policy.NativeJSON, inboundTag, userID, encodedOperation, desired)
	if err != nil {
		return err
	}
	if !changed {
		if revokeTarget {
			live, liveErr := s.pptpUserHasLiveSessions(inboundTag, userID)
			if liveErr != nil {
				return fmt.Errorf("verify PPTP target session state: %w", liveErr)
			}
			if live {
				return fmt.Errorf("PPTP policy is already applied but user %d still has a live or pending PPP session", userID)
			}
		}
		if err := s.advancePPTPMutationRevision(revision); err != nil {
			return err
		}
		return s.recordPPTPMutationCompleted(operationID, digest)
	}
	updatedPayload, err := parseNativeRuntimePayload(updatedNative)
	if err != nil {
		return err
	}
	var inbound *pptpRuntimeInbound
	for i := range updatedPayload.PPTPInbounds {
		if strings.TrimSpace(updatedPayload.PPTPInbounds[i].Tag) == inboundTag {
			inbound = &updatedPayload.PPTPInbounds[i]
			break
		}
	}
	if inbound == nil {
		return fmt.Errorf("PPTP inbound %q is absent from updated policy", inboundTag)
	}
	s.mu.Lock()
	runtime := s.pptpRuntimes[inboundTag]
	active := runtime != nil
	s.mu.Unlock()
	if !active || !pptpMutationRuntimeActive() {
		return fmt.Errorf("PPTP inbound %q has no live managed runtime", inboundTag)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pptpMutationCheckpoint(s, checkpointCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("checkpoint PPP accounting before PPTP user mutation: %w", err)
	}
	files, transitioning, err := s.preparePPTPMutationFiles(*inbound, updatedPayload.SessionCallback)
	if err != nil {
		return err
	}
	transitionPath := s.pptpSessionConfigPath(inboundTag)
	if _, ok := files[transitionPath]; !ok {
		return fmt.Errorf("PPTP mutation is missing the session policy file for inbound %q", inboundTag)
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
	if err := s.commitPPTPMutationPolicy(policy.Checksum, policy.ConfigJSON, updatedNative, revision); err != nil {
		return rollbackFiles(fmt.Errorf("persist PPTP user policy: %w", err))
	}
	if err := applyOpenVPNMutationFilesSingle(transitionPath, openVPNMutationFile{data: files[transitionPath].data}); err != nil {
		rollbackPolicyErr := s.restorePPTPMutationPolicy(policy, updatedNative, revision)
		rollbackFilesErr := restoreOpenVPNMutationFiles(allBackups)
		return errors.Join(fmt.Errorf("activate PPTP user policy: %w", err), rollbackPolicyErr, rollbackFilesErr)
	}
	s.wakeLocalQuota("pptp")
	if revokeTarget {
		if err := s.terminatePPTPUserSessions(ctx, inboundTag, userID); err != nil {
			return fmt.Errorf("PPTP policy applied but targeted session revoke failed for user %d: %w", userID, err)
		}
	}
	if err := s.recordPPTPMutationCompleted(operationID, digest); err != nil {
		return err
	}
	s.mu.Lock()
	if revision > s.appliedRev {
		s.appliedRev = revision
	}
	s.mu.Unlock()
	s.appendLog(fmt.Sprintf("PPTP user mutation committed operation=%s inbound=%s user_id=%d action=%s", operationID, inboundTag, userID, encodedOperation))
	return nil
}

// pptpSessionConfigPath returns the live session-helper path for an inbound.
func (s *Server) pptpSessionConfigPath(inboundTag string) string {
	return filepath.Join(s.cfg.DataDir, "pptp", pptpRuntimeDirName(inboundTag), "session-helper.json")
}

func pptpMutationNeedsTargetDisconnect(operation string, desired, previous *pptpRuntimeUser) bool {
	if operation == "remove" || (desired != nil && !pptpUserAllowed(*desired)) {
		return true
	}
	return operation == "update" && desired != nil && previous != nil && strings.TrimSpace(desired.IPv4Address) != strings.TrimSpace(previous.IPv4Address)
}

func pptpPolicyUser(nativeJSON, inboundTag string, userID int64) (*pptpRuntimeUser, error) {
	payload, err := parseNativeRuntimePayload(nativeJSON)
	if err != nil {
		return nil, err
	}
	for _, inbound := range payload.PPTPInbounds {
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

func (s *Server) preparePPTPMutationFiles(inbound pptpRuntimeInbound, callback nativeRuntimeSessionCallback) (map[string]openVPNMutationFile, nativeSessionHelperConfig, error) {
	tag := strings.TrimSpace(inbound.Tag)
	root := filepath.Join(s.cfg.DataDir, "pptp", pptpRuntimeDirName(tag))
	files := pptpRuntimeFiles{
		Config:        filepath.Join(root, "pptpd.conf"),
		PPPOptions:    filepath.Join(root, "ppp-options"),
		IPUpScript:    filepath.Join(root, "ip-up.sh"),
		IPDownScript:  filepath.Join(root, "ip-down.sh"),
		SessionConfig: filepath.Join(root, "session-helper.json"),
		UsageConfig:   filepath.Join(root, "usage-helper.json"),
	}
	rawCurrent, err := os.ReadFile(files.SessionConfig)
	if err != nil {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("read current PPTP session policy: %w", err)
	}
	var current nativeSessionHelperConfig
	if err := json.Unmarshal(rawCurrent, &current); err != nil {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("decode current PPTP session policy: %w", err)
	}
	if current.InboundTag != tag || current.Protocol != "pptp" {
		return nil, nativeSessionHelperConfig{}, fmt.Errorf("current PPTP session policy identity mismatch")
	}
	rendered, err := s.renderPPTPInboundFiles(inbound, files, callback)
	if err != nil {
		return nil, nativeSessionHelperConfig{}, err
	}
	current.StateDir = rendered.StateDir
	globalCHAP, err := os.ReadFile(pptpCHAPSecretsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nativeSessionHelperConfig{}, err
	}
	globalCHAP = replaceL2TPManagedBlock(globalCHAP, pptpCHAPBlockStart, pptpCHAPBlockEnd, dedupePPTPCHAPSecrets(rendered.CHAPSecrets))
	// Only stage the files whose content is admission/session/accounting
	// relevant to a user mutation. The pptpd config, PPP options and the
	// executable session hooks are identity inputs, not per-user inputs, so
	// touching them would risk clobbering their exec bits for no benefit.
	staged := map[string]openVPNMutationFile{
		files.UsageConfig:   {data: rendered.UsageConfig},
		files.SessionConfig: {data: rendered.SessionConfig},
		pptpCHAPSecretsPath: {data: globalCHAP},
	}
	return staged, current, nil
}

func (s *Server) commitPPTPMutationPolicy(expectedChecksum, configJSON, nativeJSON string, revision uint64) error {
	if pptpMutationCommitOverride != nil {
		return pptpMutationCommitOverride(s, expectedChecksum, configJSON, nativeJSON, revision)
	}
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy disappeared before PPTP mutation commit")
		}
		return err
	}
	if current.Checksum != expectedChecksum {
		return fmt.Errorf("PPTP mutation snapshot changed before commit")
	}
	if revision < current.Revision {
		return fmt.Errorf("stale PPTP mutation revision %d; current revision is %d", revision, current.Revision)
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{ConfigJson: configJSON, OvRuntimeJson: nativeJSON, DesiredRevision: revision})
}

func (s *Server) advancePPTPMutationRevision(revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy is unavailable during PPTP operation replay")
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

func (s *Server) restorePPTPMutationPolicy(previous *runtimePolicySnapshot, updatedNative string, revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		return err
	}
	if current.NativeJSON != updatedNative || current.Revision != revision {
		return fmt.Errorf("refuse PPTP policy rollback after a newer mutation")
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{ConfigJson: previous.ConfigJSON, OvRuntimeJson: previous.NativeJSON, DesiredRevision: previous.Revision})
}

func (s *Server) pptpUserHasLiveSessions(inboundTag string, userID int64) (bool, error) {
	runtimeRoot := filepath.Join(s.cfg.DataDir, "pptp", pptpRuntimeDirName(inboundTag))
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
			return false, fmt.Errorf("decode active PPTP session: %w", err)
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

func (s *Server) terminatePPTPUserSessions(ctx context.Context, inboundTag string, userID int64) error {
	root := filepath.Join(s.cfg.DataDir, "pptp", pptpRuntimeDirName(inboundTag))
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
			return fmt.Errorf("decode PPTP active session: %w", err)
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
		if err := pptpMutationTerminateSession(ctx, session.Process); err != nil {
			// The process can exit after the liveness check but before pidfd_open.
			if !pppOfflineProcessIsLive(session.Process) {
				continue
			}
			return fmt.Errorf("terminate target PPP process: %w", err)
		}
	}
	return nil
}
