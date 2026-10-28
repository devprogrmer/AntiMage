package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

var anyConnectMutationCheckpoint = (*Server).checkpointAnyConnectOffline

func anyConnectRemoveUserIdentity(value string) (int64, uint64, string, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), anyConnectRemoveUserPrefix), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, "", false
	}
	userID, errID := strconv.ParseInt(parts[0], 10, 64)
	revision, errRevision := strconv.ParseUint(parts[1], 10, 64)
	username := ""
	var errUsername error
	if len(parts) == 3 {
		var decoded []byte
		decoded, errUsername = base64.RawURLEncoding.DecodeString(parts[2])
		username = string(decoded)
	}
	return userID, revision, username, errID == nil && userID > 0 && errRevision == nil && revision > 0 && errUsername == nil
}

func decodeAnyConnectMutationUser(user *nodev1.InboundUser, envelopeOperation string) (int64, uint64, string, string, *anyConnectRuntimeUser, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "anyconnect") {
		return 0, 0, "", "", nil, fmt.Errorf("AnyConnect mutation protocol is required")
	}
	fields := user.GetFields()
	parsedID, err := strconv.ParseInt(strings.TrimSpace(fields[anyConnectMutationUserIDField]), 10, 64)
	if err != nil || parsedID <= 0 {
		return 0, 0, "", "", nil, fmt.Errorf("AnyConnect user id is invalid")
	}
	revision, err := strconv.ParseUint(strings.TrimSpace(fields[anyConnectMutationRevisionField]), 10, 64)
	if err != nil || revision == 0 {
		return 0, 0, "", "", nil, fmt.Errorf("AnyConnect desired revision is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[anyConnectMutationOpField]))
	if operation != strings.ToLower(strings.TrimSpace(envelopeOperation)) {
		return 0, 0, "", "", nil, fmt.Errorf("AnyConnect operation envelope mismatch")
	}
	var desired *anyConnectRuntimeUser
	if operation == "remove" {
		if strings.TrimSpace(fields[anyConnectMutationUserField]) != "" {
			return 0, 0, "", "", nil, fmt.Errorf("AnyConnect remove mutation cannot include desired user")
		}
	} else {
		var parsed anyConnectRuntimeUser
		if err := json.Unmarshal([]byte(fields[anyConnectMutationUserField]), &parsed); err != nil {
			return 0, 0, "", "", nil, fmt.Errorf("decode AnyConnect desired user: %w", err)
		}
		if parsed.UserID != parsedID {
			return 0, 0, "", "", nil, fmt.Errorf("AnyConnect desired user id does not match mutation")
		}
		desired = &parsed
	}
	username := strings.TrimSpace(fields[anyConnectMutationNameField])
	if desired != nil {
		username = strings.TrimSpace(desired.Username)
	}
	return parsedID, revision, operation, username, desired, nil
}

func anyConnectMutationDigest(inboundTag string, userID int64, operation, username string, desired *anyConnectRuntimeUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string                 `json:"inbound_tag"`
		UserID     int64                  `json:"user_id"`
		Operation  string                 `json:"operation"`
		Username   string                 `json:"username,omitempty"`
		Desired    *anyConnectRuntimeUser `json:"desired,omitempty"`
	}{inboundTag, userID, operation, username, desired})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) anyConnectMutationResultPath(operationID string) string {
	id := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "anyconnect", "mutation-results", hex.EncodeToString(id[:])+".json")
}

func (s *Server) anyConnectMutationAlreadyCompleted(operationID, digest string) (bool, error) {
	path := s.anyConnectMutationResultPath(operationID)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read AnyConnect operation receipt: %w", err)
	}
	var receipt openVPNMutationResult
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
		return false, fmt.Errorf("AnyConnect operation receipt is corrupt")
	}
	if receipt.Digest != digest {
		return false, fmt.Errorf("AnyConnect operation id was reused with a different mutation")
	}
	return true, nil
}

func (s *Server) recordAnyConnectMutationCompleted(operationID, digest string) error {
	path := s.anyConnectMutationResultPath(operationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create AnyConnect operation receipt directory: %w", err)
	}
	raw, err := json.Marshal(openVPNMutationResult{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("persist AnyConnect operation receipt: %w", err)
	}
	return nil
}

// applyAnyConnectUserMutation performs a targeted add/update/remove of a single
// ocserv user without restarting the daemon. ocserv's "plain[passwd=]" auth
// re-reads the password file on every authentication, so an atomic rename of
// the rewritten ocpasswd (plus the per-user config) takes effect immediately for
// new logins while unrelated established sessions and the daemon PID persist.
func (s *Server) applyAnyConnectUserMutation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, requestedRevision, encodedOperation, requestedUsername, desired, err := decodeAnyConnectMutationUser(user, operation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("AnyConnect operation id is required")
	}
	unlock, err := s.lockAnyConnectMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return fmt.Errorf("AnyConnect inbound tag is required")
	}
	mutationDigest, err := anyConnectMutationDigest(inboundTag, userID, encodedOperation, requestedUsername, desired)
	if err != nil {
		return err
	}
	completed, err := s.anyConnectMutationAlreadyCompleted(operationID, mutationDigest)
	if err != nil {
		return err
	}
	if completed {
		return s.advanceAnyConnectMutationRevision(requestedRevision)
	}
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load AnyConnect runtime policy: %w", err)
	}
	if err := validateRuntimePolicyRevision(policy.Revision, requestedRevision); err != nil {
		return fmt.Errorf("stale AnyConnect mutation revision: %w", err)
	}

	s.mu.Lock()
	runtime := s.anyConnectRuntimes[inboundTag]
	active := runtime != nil
	if active {
		if runtime.done == nil || runtime.cmd == nil || runtime.cmd.Process == nil {
			active = false
		} else {
			select {
			case <-runtime.done:
				active = false
			default:
			}
		}
	}
	var before anyConnectRuntimeInbound
	if active {
		before = runtime.inbound
	}
	s.mu.Unlock()
	if !active {
		return fmt.Errorf("AnyConnect inbound %q has no live managed runtime", inboundTag)
	}

	oldUser := anyConnectFindUser(before.Users, userID)
	if oldUser != nil && requestedUsername == "" {
		requestedUsername = strings.TrimSpace(oldUser.Username)
	}
	if requestedUsername == "" {
		return fmt.Errorf("AnyConnect mutation username is required to reconcile the live session")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	next := before
	next.Users = append([]anyConnectRuntimeUser(nil), before.Users...)
	changed := false
	found := false
	for i := range next.Users {
		if next.Users[i].UserID != userID {
			continue
		}
		found = true
		if encodedOperation == "remove" {
			next.Users = append(next.Users[:i], next.Users[i+1:]...)
		} else {
			next.Users[i] = *desired
		}
		changed = true
		break
	}
	if encodedOperation == "add" && !found {
		next.Users = append(next.Users, *desired)
		changed = true
	}
	if encodedOperation == "update" && !found {
		return fmt.Errorf("AnyConnect user %d is absent from the live runtime", userID)
	}
	if !changed {
		return s.recordAnyConnectMutationCompleted(operationID, mutationDigest)
	}

	if anyConnectAccountingEnabled(inboundTag, next.Settings) {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = anyConnectMutationCheckpoint(s, checkpointCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("checkpoint AnyConnect accounting before user mutation: %w", err)
		}
	}

	previousUsers, err := readFileIfExists(runtime.files.PasswordFile)
	if err != nil {
		return err
	}
	previousCCD, err := anyConnectReadUserConfigs(runtime.files.UserConfigDir)
	if err != nil {
		return err
	}

	files := runtime.files
	backups, err := s.publishAnyConnectUserFiles(next, files, userID, requestedUsername)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		restoreErr := restoreAnyConnectUserFiles(backups, previousUsers, previousCCD, files)
		s.mu.Lock()
		if s.anyConnectRuntimes[inboundTag] == runtime {
			runtime.inbound = before
		}
		s.mu.Unlock()
		return errors.Join(cause, restoreErr)
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := s.commitAnyConnectMutationPolicy(policy.ConfigJSON, inboundTag, userID, encodedOperation, desired, requestedRevision); err != nil {
		return rollback(fmt.Errorf("persist AnyConnect user policy: %w", err))
	}
	s.mu.Lock()
	if s.anyConnectRuntimes[inboundTag] == runtime {
		runtime.inbound = next
	}
	s.mu.Unlock()

	shouldDisconnect := encodedOperation == "remove"
	if desired != nil {
		allowed, _ := nativeSessionUserPolicyAllowed(anyConnectSessionPolicy(*desired), timeNow())
		shouldDisconnect = !allowed
	}
	if shouldDisconnect {
		if err := s.disconnectAnyConnectUser(inboundTag, requestedUsername); err != nil {
			return fmt.Errorf("AnyConnect policy applied but targeted session revoke failed for user %d: %w", userID, err)
		}
	}
	s.appendLog(fmt.Sprintf("AnyConnect user mutation committed operation=%s inbound=%s user_id=%d action=%s", operationID, inboundTag, userID, encodedOperation))
	if err := s.recordAnyConnectMutationCompleted(operationID, mutationDigest); err != nil {
		return err
	}
	s.mu.Lock()
	if requestedRevision > s.appliedRev {
		s.appliedRev = requestedRevision
	}
	s.mu.Unlock()
	return nil
}

func anyConnectFindUser(users []anyConnectRuntimeUser, userID int64) *anyConnectRuntimeUser {
	for i := range users {
		if users[i].UserID == userID {
			copy := users[i]
			return &copy
		}
	}
	return nil
}

func anyConnectAccountingEnabled(_ string, settings map[string]any) bool {
	value, ok := settings["accounting_enabled"]
	if !ok {
		return true
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return !strings.EqualFold(strings.TrimSpace(typed), "false")
	default:
		return true
	}
}

func (s *Server) lockAnyConnectMutation(ctx context.Context, operationID string) (func(), error) {
	index := openVPNMutationLockIndex(operationID)
	s.mu.Lock()
	lock := s.anyConnectMutationLocks[index]
	if lock == nil {
		lock = make(chan struct{}, 1)
		s.anyConnectMutationLocks[index] = lock
	}
	s.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) advanceAnyConnectMutationRevision(revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy is unavailable during AnyConnect operation replay")
		}
		return err
	}
	if err := validateRuntimePolicyRevision(current.Revision, revision); err != nil {
		return err
	}
	if revision <= current.Revision {
		return nil
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{
		ConfigJson: current.ConfigJSON, OvRuntimeJson: current.NativeJSON, DesiredRevision: revision,
	})
}

func (s *Server) commitAnyConnectMutationPolicy(configJSON, inboundTag string, userID int64, operation string, desired *anyConnectRuntimeUser, revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy disappeared before AnyConnect mutation commit")
		}
		return err
	}
	if err := validateRuntimePolicyRevision(current.Revision, revision); err != nil {
		return fmt.Errorf("stale AnyConnect mutation revision: %w", err)
	}
	updatedNative, _, err := planAnyConnectUserPolicyMutation(current.NativeJSON, inboundTag, userID, operation, desired)
	if err != nil {
		return err
	}
	if strings.TrimSpace(configJSON) == "" {
		configJSON = current.ConfigJSON
	}
	if revision < current.Revision {
		return fmt.Errorf("stale AnyConnect mutation revision %d; current revision is %d", revision, current.Revision)
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{
		ConfigJson: configJSON, OvRuntimeJson: updatedNative, DesiredRevision: revision,
	})
}
