package nodeagent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	openVPNMutationNameField     = "openvpn_username"
	openVPNMutationRevisionField = "openvpn_revision"
)

func openVPNRemoveUserIdentity(value string) (int64, uint64, string, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), openVPNRemoveUserPrefix), ":")
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

var openVPNMutationCheckpoint = (*Server).checkpointOpenVPNOffline
var openVPNMutationManagementStatus = openVPNManagementStatus
var openVPNMutationManagementClientKill = openVPNManagementClientKill

type openVPNMutationFile struct {
	data   []byte
	remove bool
}

type openVPNMutationFileBackup struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func decodeOpenVPNMutationUser(user *nodev1.InboundUser, envelopeOperation string) (int64, uint64, string, string, *openVPNRuntimeUser, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "openvpn") {
		return 0, 0, "", "", nil, fmt.Errorf("OpenVPN mutation protocol is required")
	}
	fields := user.GetFields()
	var userID int64
	parsedID, err := strconv.ParseInt(strings.TrimSpace(fields[openVPNMutationUserIDField]), 10, 64)
	if err != nil || parsedID <= 0 {
		return 0, 0, "", "", nil, fmt.Errorf("OpenVPN user id is invalid")
	}
	userID = parsedID
	revision, err := strconv.ParseUint(strings.TrimSpace(fields[openVPNMutationRevisionField]), 10, 64)
	if err != nil || revision == 0 {
		return 0, 0, "", "", nil, fmt.Errorf("OpenVPN desired revision is invalid")
	}
	operation := strings.ToLower(strings.TrimSpace(fields[openVPNMutationOpField]))
	if operation != strings.ToLower(strings.TrimSpace(envelopeOperation)) {
		return 0, 0, "", "", nil, fmt.Errorf("OpenVPN operation envelope mismatch")
	}
	var desired *openVPNRuntimeUser
	if operation == "remove" {
		if strings.TrimSpace(fields[openVPNMutationUserField]) != "" {
			return 0, 0, "", "", nil, fmt.Errorf("OpenVPN remove mutation cannot include desired user")
		}
	} else {
		var parsed openVPNRuntimeUser
		if err := json.Unmarshal([]byte(fields[openVPNMutationUserField]), &parsed); err != nil {
			return 0, 0, "", "", nil, fmt.Errorf("decode OpenVPN desired user: %w", err)
		}
		if parsed.UserID != userID {
			return 0, 0, "", "", nil, fmt.Errorf("OpenVPN desired user id does not match mutation")
		}
		desired = &parsed
	}
	username := strings.TrimSpace(fields[openVPNMutationNameField])
	if desired != nil {
		username = openVPNMutationUsername(*desired)
	}
	return userID, revision, operation, username, desired, nil
}

func openVPNMutationUsername(user openVPNRuntimeUser) string {
	if value := strings.TrimSpace(user.VPNUsername); value != "" {
		return value
	}
	return strings.TrimSpace(user.Username)
}

// applyOpenVPNUserMutation stages all generated user files away from the live
// runtime, atomically replaces only the target user's CCD plus shared auth and
// accounting policy files, then commits the native policy snapshot. The server
// config and OpenVPN process are deliberately left untouched.
func (s *Server) applyOpenVPNUserMutation(ctx context.Context, operationID, inboundTag string, user *nodev1.InboundUser, operation string) error {
	userID, requestedRevision, encodedOperation, requestedUsername, desired, err := decodeOpenVPNMutationUser(user, operation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("OpenVPN mutation operation id is required")
	}
	unlock, err := s.lockOpenVPNMutation(ctx, operationID)
	if err != nil {
		return err
	}
	defer unlock()
	inboundTag = strings.TrimSpace(inboundTag)
	if inboundTag == "" {
		return fmt.Errorf("OpenVPN inbound tag is required")
	}
	mutationDigest, err := openVPNMutationDigest(inboundTag, userID, encodedOperation, requestedUsername, desired)
	if err != nil {
		return err
	}
	completed, err := s.openVPNMutationAlreadyCompleted(operationID, mutationDigest)
	if err != nil {
		return err
	}
	if completed {
		return s.advanceOpenVPNMutationRevision(requestedRevision)
	}
	policy, err := s.currentRuntimePolicy()
	if err != nil || policy == nil {
		if err == nil {
			err = fmt.Errorf("no committed runtime policy is available")
		}
		return fmt.Errorf("load OpenVPN runtime policy: %w", err)
	}
	if err := validateRuntimePolicyRevision(policy.Revision, requestedRevision); err != nil {
		return fmt.Errorf("stale OpenVPN mutation revision: %w", err)
	}
	updatedNative, _, err := planOpenVPNUserPolicyMutation(policy.NativeJSON, inboundTag, userID, encodedOperation, desired)
	if err != nil {
		return err
	}
	payload, err := parseNativeRuntimePayload(updatedNative)
	if err != nil {
		return err
	}
	var inbound *openVPNRuntimeInbound
	for i := range payload.OpenVPNInbounds {
		if strings.TrimSpace(payload.OpenVPNInbounds[i].Tag) == inboundTag {
			inbound = &payload.OpenVPNInbounds[i]
			break
		}
	}
	if inbound == nil {
		return fmt.Errorf("OpenVPN inbound %q is absent from updated policy", inboundTag)
	}
	oldPayload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil {
		return err
	}
	var oldUser *openVPNRuntimeUser
	for i := range oldPayload.OpenVPNInbounds {
		if strings.TrimSpace(oldPayload.OpenVPNInbounds[i].Tag) != inboundTag {
			continue
		}
		for j := range oldPayload.OpenVPNInbounds[i].Users {
			if oldPayload.OpenVPNInbounds[i].Users[j].UserID == userID {
				copy := oldPayload.OpenVPNInbounds[i].Users[j]
				oldUser = &copy
				break
			}
		}
	}
	if oldUser != nil && requestedUsername == "" {
		requestedUsername = openVPNMutationUsername(*oldUser)
	}
	if requestedUsername == "" {
		return fmt.Errorf("OpenVPN mutation username is required to reconcile the live session")
	}

	s.mu.Lock()
	runtime := s.openVPNRuntimes[inboundTag]
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
	s.mu.Unlock()
	if !active {
		return fmt.Errorf("OpenVPN inbound %q has no live managed runtime", inboundTag)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if openVPNBoolSetting(inbound.Settings, "accounting_enabled", true) {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = openVPNMutationCheckpoint(s, checkpointCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("checkpoint OpenVPN accounting before user mutation: %w", err)
		}
	}
	files, err := prepareOpenVPNMutationFiles(s.cfg.DataDir, *inbound, payload.SessionCallback, oldUser, desired)
	if err != nil {
		return err
	}
	backups, err := applyOpenVPNMutationFiles(files)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		return errors.Join(cause, restoreOpenVPNMutationFiles(backups))
	}
	if err := ctx.Err(); err != nil {
		return rollback(err)
	}
	if err := s.commitOpenVPNMutationPolicy(policy.ConfigJSON, inboundTag, userID, encodedOperation, desired, requestedRevision); err != nil {
		return rollback(fmt.Errorf("persist OpenVPN user policy: %w", err))
	}
	shouldDisconnect := encodedOperation == "remove"
	if desired != nil {
		allowed, _ := nativeSessionUserPolicyAllowed(openVPNRuntimePolicy(*desired), time.Now().UTC())
		shouldDisconnect = !allowed
	}
	if shouldDisconnect {
		if err := s.disconnectOpenVPNUser(inboundTag, *inbound, requestedUsername); err != nil {
			return fmt.Errorf("OpenVPN policy applied but targeted session revoke failed for user %d: %w", userID, err)
		}
	}
	s.appendLog(fmt.Sprintf("OpenVPN user mutation committed operation=%s inbound=%s user_id=%d action=%s", operationID, inboundTag, userID, encodedOperation))
	return s.recordOpenVPNMutationCompleted(operationID, mutationDigest)
}

type openVPNMutationResult struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Done    bool   `json:"done"`
}

func openVPNMutationLockIndex(operationID string) uint8 {
	sum := sha256.Sum256([]byte(operationID))
	return sum[0] % 32
}

func (s *Server) lockOpenVPNMutation(ctx context.Context, operationID string) (func(), error) {
	index := openVPNMutationLockIndex(operationID)
	s.mu.Lock()
	lock := s.openVPNMutationLocks[index]
	if lock == nil {
		lock = make(chan struct{}, 1)
		s.openVPNMutationLocks[index] = lock
	}
	s.mu.Unlock()
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func openVPNMutationDigest(inboundTag string, userID int64, operation, username string, desired *openVPNRuntimeUser) (string, error) {
	raw, err := json.Marshal(struct {
		InboundTag string              `json:"inbound_tag"`
		UserID     int64               `json:"user_id"`
		Operation  string              `json:"operation"`
		Username   string              `json:"username,omitempty"`
		Desired    *openVPNRuntimeUser `json:"desired,omitempty"`
	}{inboundTag, userID, operation, username, desired})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) advanceOpenVPNMutationRevision(revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy is unavailable during OpenVPN operation replay")
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

func (s *Server) openVPNMutationResultPath(operationID string) string {
	id := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.cfg.DataDir, "openvpn", "mutation-results", hex.EncodeToString(id[:])+".json")
}

func (s *Server) openVPNMutationAlreadyCompleted(operationID, digest string) (bool, error) {
	path := s.openVPNMutationResultPath(operationID)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read OpenVPN operation receipt: %w", err)
	}
	var receipt openVPNMutationResult
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Version != 1 || !receipt.Done || len(receipt.Digest) != sha256.Size*2 {
		return false, fmt.Errorf("OpenVPN operation receipt is corrupt")
	}
	if receipt.Digest != digest {
		return false, fmt.Errorf("OpenVPN operation id was reused with a different mutation")
	}
	return true, nil
}

func (s *Server) recordOpenVPNMutationCompleted(operationID, digest string) error {
	path := s.openVPNMutationResultPath(operationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create OpenVPN operation receipt directory: %w", err)
	}
	raw, err := json.Marshal(openVPNMutationResult{Version: 1, Digest: digest, Done: true})
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, raw, 0600); err != nil {
		return fmt.Errorf("persist OpenVPN operation receipt: %w", err)
	}
	return nil
}

func (s *Server) commitOpenVPNMutationPolicy(configJSON, inboundTag string, userID int64, operation string, desired *openVPNRuntimeUser, revision uint64) error {
	s.runtimePolicyMu.Lock()
	defer s.runtimePolicyMu.Unlock()
	current, err := s.currentRuntimePolicy()
	if err != nil || current == nil {
		if err == nil {
			err = fmt.Errorf("runtime policy disappeared before OpenVPN mutation commit")
		}
		return err
	}
	if err := validateRuntimePolicyRevision(current.Revision, revision); err != nil {
		return fmt.Errorf("stale OpenVPN mutation revision: %w", err)
	}
	updatedNative, _, err := planOpenVPNUserPolicyMutation(current.NativeJSON, inboundTag, userID, operation, desired)
	if err != nil {
		return err
	}
	if strings.TrimSpace(configJSON) == "" {
		configJSON = current.ConfigJSON
	}
	if revision < current.Revision {
		return fmt.Errorf("stale OpenVPN mutation revision %d; current revision is %d", revision, current.Revision)
	}
	return s.persistRuntimePolicyLocked(&nodev1.RuntimeConfigRequest{
		ConfigJson: configJSON, OvRuntimeJson: updatedNative, DesiredRevision: revision,
	})
}

func openVPNRuntimePolicy(user openVPNRuntimeUser) nativeSessionUserPolicy {
	var limit, expire int64
	if user.DataLimit != nil {
		limit = *user.DataLimit
	}
	if user.Expire != nil {
		expire = *user.Expire
	}
	return nativeSessionUserPolicy{Status: user.Status, UsedTraffic: user.UsedTraffic, DataLimit: limit, Expire: expire,
		DeviceLimit: user.DeviceLimit, IPLimit: user.IPLimit, UploadSpeedLimit: user.UploadSpeedLimit,
		DownloadSpeedLimit: user.DownloadSpeedLimit, ReflectedUsageBatchID: user.ReflectedUsageBatchID,
		UsageCoefficient: user.UsageCoefficient, InboundCoefficient: user.InboundCoefficient}
}

func prepareOpenVPNMutationFiles(dataDir string, inbound openVPNRuntimeInbound, callback nativeRuntimeSessionCallback, oldUser, desired *openVPNRuntimeUser) (map[string]openVPNMutationFile, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("OpenVPN data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create OpenVPN data directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(dataDir, ".openvpn-mutation-stage-")
	if err != nil {
		return nil, fmt.Errorf("create OpenVPN mutation staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stage := New(Config{DataDir: stageDir})
	stageConfig, err := stage.prepareOpenVPNInbound(inbound, callback)
	if err != nil {
		return nil, fmt.Errorf("prepare OpenVPN user files: %w", err)
	}
	stageRoot := filepath.Dir(stageConfig)
	root := filepath.Join(dataDir, "openvpn", openVPNRuntimeDirName(inbound.Tag))
	files := make(map[string]openVPNMutationFile, 5)
	copyStaged := func(name, destination string, rewrite func([]byte) ([]byte, error)) error {
		raw, readErr := os.ReadFile(filepath.Join(stageRoot, name))
		if os.IsNotExist(readErr) {
			files[destination] = openVPNMutationFile{remove: true}
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if rewrite != nil {
			raw, readErr = rewrite(raw)
			if readErr != nil {
				return readErr
			}
		}
		files[destination] = openVPNMutationFile{data: raw}
		return nil
	}
	if err := copyStaged("credentials.sha256", filepath.Join(root, "credentials.sha256"), nil); err != nil {
		return nil, err
	}
	if err := copyStaged("usage-helper.json", filepath.Join(root, "usage-helper.json"), func(raw []byte) ([]byte, error) {
		var usage openVPNUsageRuntimeConfig
		if err := json.Unmarshal(raw, &usage); err != nil {
			return nil, err
		}
		usage.StatusFile = filepath.Join(root, "status.tsv")
		return json.Marshal(usage)
	}); err != nil {
		return nil, fmt.Errorf("stage OpenVPN accounting helper: %w", err)
	}
	if err := copyStaged("session-helper.json", filepath.Join(root, "session-helper.json"), func(raw []byte) ([]byte, error) {
		var session nativeSessionHelperConfig
		if err := json.Unmarshal(raw, &session); err != nil {
			return nil, err
		}
		session.StateDir = filepath.Join(root, "sessions")
		if strings.EqualFold(session.ManagementNetwork, "unix") {
			session.ManagementAddress = filepath.Join(root, "management.sock")
		}
		return json.Marshal(session)
	}); err != nil {
		return nil, fmt.Errorf("stage OpenVPN session helper: %w", err)
	}
	usernames := map[string]struct{}{}
	if oldUser != nil {
		if username := openVPNMutationUsername(*oldUser); username != "" {
			usernames[username] = struct{}{}
		}
	}
	if desired != nil {
		if username := openVPNMutationUsername(*desired); username != "" {
			usernames[username] = struct{}{}
		}
	}
	for username := range usernames {
		if err := validateOpenVPNUsername(username); err != nil {
			return nil, fmt.Errorf("invalid OpenVPN mutation username: %w", err)
		}
		destination := filepath.Join(root, "ccd", username)
		source := filepath.Join(stageRoot, "ccd", username)
		raw, readErr := os.ReadFile(source)
		if os.IsNotExist(readErr) {
			files[destination] = openVPNMutationFile{remove: true}
		} else if readErr != nil {
			return nil, readErr
		} else {
			files[destination] = openVPNMutationFile{data: raw}
		}
	}
	return files, nil
}

func applyOpenVPNMutationFiles(files map[string]openVPNMutationFile) ([]openVPNMutationFileBackup, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	backups := make([]openVPNMutationFileBackup, 0, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		backup := openVPNMutationFileBackup{path: path, mode: 0600}
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("refuse to mutate non-regular OpenVPN file %s", filepath.Base(path))
			}
			backup.mode, backup.exists = info.Mode().Perm(), true
			backup.data, err = os.ReadFile(path)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		backups = append(backups, backup)
	}
	for i, path := range paths {
		change := files[path]
		var err error
		if change.remove {
			err = os.Remove(path)
			if os.IsNotExist(err) {
				err = nil
			}
		} else {
			err = os.MkdirAll(filepath.Dir(path), 0700)
			if err == nil {
				err = atomicWriteFile(path, change.data, 0600)
			}
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("apply staged OpenVPN file %s: %w", filepath.Base(path), err), restoreOpenVPNMutationFiles(backups[:i+1]))
		}
	}
	return backups, nil
}

func restoreOpenVPNMutationFiles(backups []openVPNMutationFileBackup) error {
	var errs []error
	for i := len(backups) - 1; i >= 0; i-- {
		backup := backups[i]
		if !backup.exists {
			if err := os.Remove(backup.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := atomicWriteFile(backup.path, backup.data, backup.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) disconnectOpenVPNUser(tag string, inbound openVPNRuntimeInbound, username string) error {
	root := filepath.Join(s.cfg.DataDir, "openvpn", openVPNRuntimeDirName(tag))
	network, address := "unix", filepath.Join(root, "management.sock")
	if port := openVPNIntSetting(inbound.Settings, "management_port"); port > 0 && port <= 65535 {
		network, address = "tcp", fmt.Sprintf("127.0.0.1:%d", port)
	}
	clients, err := openVPNMutationManagementStatus(network, address)
	if err != nil {
		return err
	}
	for _, client := range clients {
		if !strings.EqualFold(strings.TrimSpace(client.Username), username) && !strings.EqualFold(strings.TrimSpace(client.CommonName), username) {
			continue
		}
		if err := openVPNMutationManagementClientKill(network, address, client.ClientID); err != nil {
			return err
		}
	}
	return nil
}

func openVPNManagementStatus(network, address string) ([]openVPNStatusClient, error) {
	conn, err := net.DialTimeout(network, address, openVPNManagementTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect OpenVPN management interface: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(openVPNManagementTimeout)); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(conn, "status 3\n"); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(conn, 256*1024))
	var raw strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		_, _ = raw.WriteString(line)
		if strings.TrimSpace(line) == "END" {
			break
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, fmt.Errorf("read OpenVPN management status: %w", readErr)
			}
			return nil, fmt.Errorf("OpenVPN management status response is incomplete")
		}
	}
	clients, err := parseOpenVPNStatusV3(raw.String())
	if err != nil {
		return nil, fmt.Errorf("parse OpenVPN management status: %w", err)
	}
	return clients, nil
}
