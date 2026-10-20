package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	ikev2MutationUserIDField = "ikev2_user_id"
	ikev2MutationOperation   = "ikev2_operation"
	ikev2MutationRevision    = "ikev2_revision"
	ikev2MutationUserJSON    = "ikev2_user_json"
	ikev2RemoveUserPrefix    = "ikev2-user:"
)

func decodeIKEv2MutationUser(user *nodev1.InboundUser, envelope string) (int64, uint64, string, *ikev2RuntimeUser, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "ikev2") {
		return 0, 0, "", nil, fmt.Errorf("IKEv2 mutation protocol is required")
	}
	f := user.GetFields()
	id, err := strconv.ParseInt(strings.TrimSpace(f[ikev2MutationUserIDField]), 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, "", nil, fmt.Errorf("IKEv2 user id is invalid")
	}
	rev, err := strconv.ParseUint(strings.TrimSpace(f[ikev2MutationRevision]), 10, 64)
	if err != nil || rev == 0 {
		return 0, 0, "", nil, fmt.Errorf("IKEv2 desired revision is invalid")
	}
	op := strings.ToLower(strings.TrimSpace(f[ikev2MutationOperation]))
	if op != strings.ToLower(strings.TrimSpace(envelope)) {
		return 0, 0, "", nil, fmt.Errorf("IKEv2 operation envelope mismatch")
	}
	if op == "remove" {
		if strings.TrimSpace(f[ikev2MutationUserJSON]) != "" {
			return 0, 0, "", nil, fmt.Errorf("IKEv2 remove mutation cannot include desired user")
		}
		return id, rev, op, nil, nil
	}
	var desired ikev2RuntimeUser
	if err := json.Unmarshal([]byte(f[ikev2MutationUserJSON]), &desired); err != nil {
		return 0, 0, "", nil, fmt.Errorf("decode IKEv2 desired user: %w", err)
	}
	if desired.UserID != id {
		return 0, 0, "", nil, fmt.Errorf("IKEv2 desired user id does not match mutation")
	}
	return id, rev, op, &desired, nil
}

func ikev2RemoveUserIdentity(value string) (int64, uint64, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), ikev2RemoveUserPrefix), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	id, e1 := strconv.ParseInt(parts[0], 10, 64)
	rev, e2 := strconv.ParseUint(parts[1], 10, 64)
	return id, rev, id > 0 && e1 == nil && rev > 0 && e2 == nil
}

func ikev2MutationDigest(tag string, id int64, op string, desired *ikev2RuntimeUser) string {
	raw, _ := json.Marshal(struct {
		Tag     string
		ID      int64
		Op      string
		Desired *ikev2RuntimeUser
	}{tag, id, op, desired})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Server) ikev2MutationReceiptPath(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.cfg.DataDir, "ikev2", "mutation-results", hex.EncodeToString(sum[:])+".json")
}
func (s *Server) ikev2MutationAlreadyCompleted(id, digest string) (bool, error) {
	raw, err := os.ReadFile(s.ikev2MutationReceiptPath(id))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r openVPNMutationResult
	if json.Unmarshal(raw, &r) != nil || !r.Done || r.Digest != digest {
		return false, fmt.Errorf("IKEv2 operation id was reused with a different mutation")
	}
	return true, nil
}
func (s *Server) recordIKEv2MutationCompleted(id, digest string) error {
	p := s.ikev2MutationReceiptPath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	raw, _ := json.Marshal(openVPNMutationResult{Version: 1, Done: true, Digest: digest})
	return atomicWriteFile(p, raw, 0600)
}

func (s *Server) applyIKEv2UserMutation(ctx context.Context, operationID, tag string, user *nodev1.InboundUser, operation string) error {
	id, rev, op, desired, err := decodeIKEv2MutationUser(user, operation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("IKEv2 operation id is required")
	}
	digest := ikev2MutationDigest(tag, id, op, desired)
	done, err := s.ikev2MutationAlreadyCompleted(operationID, digest)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	s.mu.Lock()
	proc := s.ikev2Runtimes[tag]
	if proc == nil {
		s.mu.Unlock()
		return fmt.Errorf("IKEv2 inbound %q is not active", tag)
	}
	before := proc.inbound
	next := before
	next.Users = append([]ikev2RuntimeUser(nil), before.Users...)
	s.mu.Unlock()
	found := false
	for i := range next.Users {
		if next.Users[i].UserID != id {
			continue
		}
		found = true
		if op == "remove" {
			next.Users = append(next.Users[:i], next.Users[i+1:]...)
		} else {
			next.Users[i] = *desired
		}
		break
	}
	if op == "add" && !found {
		next.Users = append(next.Users, *desired)
		found = true
	}
	if op != "add" && !found && op != "remove" {
		return fmt.Errorf("IKEv2 user %d not found", id)
	}
	if op == "remove" && !found {
		return s.recordIKEv2MutationCompleted(operationID, digest)
	}
	if desired != nil && desired.UserID != id {
		return fmt.Errorf("IKEv2 user id mismatch")
	}
	_ = rev
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		return err
	}
	oldSecrets, err := readFileIfExists(l2TPIPSecSecretsPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	proc.inbound = next
	s.mu.Unlock()
	restore := func() {
		_ = atomicWriteFile(l2TPIPSecSecretsPath, oldSecrets, 0600)
		s.mu.Lock()
		proc.inbound = before
		s.mu.Unlock()
		_ = runL2TPCommand(s, "ipsec", "rereadsecrets")
		_ = runL2TPCommand(s, "ipsec", "reload")
	}
	if err := s.installIKEv2SecretsFromRuntimes(); err != nil {
		s.mu.Lock()
		proc.inbound = before
		s.mu.Unlock()
		return err
	}
	if err := runL2TPCommand(s, "ipsec", "rereadsecrets"); err != nil {
		restore()
		return err
	}
	if err := runL2TPCommand(s, "ipsec", "reload"); err != nil {
		restore()
		return err
	}
	if err := s.recordIKEv2MutationCompleted(operationID, digest); err != nil {
		restore()
		return err
	}
	return nil
}

func (s *Server) installIKEv2SecretsFromRuntimes() error {
	s.mu.Lock()
	runtimes := make([]preparedIKEv2Runtime, 0, len(s.ikev2Runtimes))
	for _, p := range s.ikev2Runtimes {
		runtimes = append(runtimes, preparedIKEv2Runtime{Tag: p.tag, Inbound: p.inbound, Files: p.files})
	}
	s.mu.Unlock()
	var b strings.Builder
	for i, r := range runtimes {
		if i > 0 {
			b.WriteByte('\n')
		}
		sec, err := renderIKEv2IPSecSecrets(r.Inbound, r.Files)
		if err != nil {
			return err
		}
		b.WriteString(sec)
	}
	return updateL2TPManagedBlock(l2TPIPSecSecretsPath, ikev2SecretBlockStart, ikev2SecretBlockEnd, b.String())
}
