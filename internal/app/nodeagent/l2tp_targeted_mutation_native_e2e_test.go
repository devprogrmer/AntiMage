//go:build linux

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// TestL2TPTargetedMutationNativeStage is driven by native-l2tp-driver.sh while
// a real xl2tpd/pppd pair and one or two kernel PPP sessions are active.
func TestL2TPTargetedMutationNativeStage(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_NATIVE_STATE"))
	action := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_MUTATION_ACTION"))
	if stateDir == "" || action == "" {
		t.Skip("requires the privileged native L2TP mutation harness")
	}
	serverPID := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_XL2TPD_PID"))
	if serverPID == "" {
		t.Fatal("native xl2tpd PID is required")
	}
	serverIdentity, err := offlineLiveProcessIdentity(serverPID)
	if err != nil {
		t.Fatalf("read native xl2tpd identity before mutation: %v", err)
	}

	server, policy, inbound, runtimeRoot := nativeL2TPMutationServer(t, stateDir)
	aliceBefore, err := nativeL2TPActiveUser(runtimeRoot, 7)
	if err != nil {
		t.Fatalf("find existing Alice PPP session: %v", err)
	}
	aliceInterfaceIdentity, err := pppOfflineReadIdentity(aliceBefore.Interface)
	if err != nil || aliceInterfaceIdentity != aliceBefore.Identity {
		t.Fatalf("Alice PPP interface identity is not live: record=%+v identity=%q err=%v", aliceBefore, aliceInterfaceIdentity, err)
	}

	assertRuntimeUnchanged := func() {
		t.Helper()
		current, err := offlineLiveProcessIdentity(serverPID)
		if err != nil || current != serverIdentity {
			t.Fatalf("xl2tpd process identity changed during targeted mutation: before=%s after=%s err=%v", serverIdentity, current, err)
		}
		identity, err := pppOfflineReadIdentity(aliceBefore.Interface)
		if err != nil || identity != aliceInterfaceIdentity {
			t.Fatalf("unrelated Alice PPP interface changed: before=%s after=%s err=%v", aliceInterfaceIdentity, identity, err)
		}
		if live, err := nativeL2TPActiveUser(runtimeRoot, 7); err != nil || live.Process != aliceBefore.Process {
			t.Fatalf("unrelated Alice session metadata changed: before=%+v after=%+v err=%v", aliceBefore, live, err)
		}
	}

	switch action {
	case "add":
		bob := l2TPRuntimeUser{UserID: 8, Username: "native-l2tp-bob", VPNUsername: "native-l2tp-bob", Password: "native-l2tp-bob-v1", IPv4Address: "10.67.0.3", Status: "active"}
		request := &nodev1.InboundUserRequest{OperationId: "native-l2tp-add-bob", InboundTag: inbound.Tag, User: nativeL2TPMutationUser(bob, "add", policy.Revision+1)}
		response, err := server.AddUser(context.Background(), request)
		if err != nil || response == nil || !response.GetAccepted() || response.GetOperationId() != request.GetOperationId() {
			t.Fatalf("real NodeAgent AddUser RPC handler failed: response=%v err=%v", response, err)
		}
		afterAdd, err := server.currentRuntimePolicy()
		if err != nil || afterAdd == nil || afterAdd.Revision != policy.Revision+1 {
			t.Fatalf("AddUser did not persist exactly one policy revision: before=%d after=%v err=%v", policy.Revision, afterAdd, err)
		}
		retryResponse, err := server.AddUser(context.Background(), request)
		if err != nil || retryResponse == nil || !retryResponse.GetAccepted() {
			t.Fatalf("same-operation AddUser retry was not idempotent: %v", err)
		}
		assertL2TPNativePolicyRevision(t, server, afterAdd.Revision)
		conflict := bob
		conflict.Password = "conflicting-secret"
		_, err = server.AddUser(context.Background(), &nodev1.InboundUserRequest{OperationId: request.OperationId, InboundTag: inbound.Tag, User: nativeL2TPMutationUser(conflict, "add", policy.Revision+1)})
		if err == nil || !strings.Contains(err.Error(), "reused with a different mutation") {
			t.Fatalf("conflicting operation id was not rejected: %v", err)
		}
		assertL2TPNativeCredential(t, runtimeRoot, bob)
		assertRuntimeUnchanged()
	case "update":
		bob, err := l2TPPolicyUser(policy.NativeJSON, inbound.Tag, 8)
		if err != nil || bob == nil {
			t.Fatalf("read added Bob from durable policy: user=%+v err=%v", bob, err)
		}
		bob.Password = "native-l2tp-bob-v2"
		before, err := nativeL2TPActiveUser(runtimeRoot, bob.UserID)
		if err != nil {
			t.Fatalf("find live Bob PPP session before update: %v", err)
		}
		beforeIdentity, err := pppOfflineReadIdentity(before.Interface)
		if err != nil || beforeIdentity != before.Identity {
			t.Fatalf("Bob PPP interface is not live before update: %+v identity=%s err=%v", before, beforeIdentity, err)
		}
		request := &nodev1.InboundUserRequest{OperationId: "native-l2tp-update-bob", InboundTag: inbound.Tag, User: nativeL2TPMutationUser(*bob, "update", policy.Revision+1)}
		response, err := server.UpdateUser(context.Background(), request)
		if err != nil || response == nil || !response.GetAccepted() {
			t.Fatalf("real NodeAgent UpdateUser RPC handler failed: %v", err)
		}
		afterUpdate, err := server.currentRuntimePolicy()
		if err != nil || afterUpdate == nil || afterUpdate.Revision != policy.Revision+1 {
			t.Fatalf("UpdateUser did not persist exactly one policy revision: before=%d after=%v err=%v", policy.Revision, afterUpdate, err)
		}
		retryResponse, err := server.UpdateUser(context.Background(), request)
		if err != nil || retryResponse == nil || !retryResponse.GetAccepted() {
			t.Fatalf("same-operation UpdateUser retry was not idempotent: %v", err)
		}
		assertL2TPNativePolicyRevision(t, server, afterUpdate.Revision)
		assertL2TPNativeCredential(t, runtimeRoot, *bob)
		after, err := nativeL2TPActiveUser(runtimeRoot, bob.UserID)
		if err != nil || after.Process != before.Process || after.Interface != before.Interface {
			t.Fatalf("credential-only update disturbed Bob's active PPP session: before=%+v after=%+v err=%v", before, after, err)
		}
		afterIdentity, err := pppOfflineReadIdentity(after.Interface)
		if err != nil || afterIdentity != beforeIdentity {
			t.Fatalf("credential-only update changed Bob's interface identity: before=%s after=%s err=%v", beforeIdentity, afterIdentity, err)
		}
		assertRuntimeUnchanged()
	case "remove":
		bob, err := l2TPPolicyUser(policy.NativeJSON, inbound.Tag, 8)
		if err != nil || bob == nil {
			t.Fatalf("read Bob from durable policy before removal: user=%+v err=%v", bob, err)
		}
		before, err := nativeL2TPActiveUser(runtimeRoot, bob.UserID)
		if err != nil {
			t.Fatalf("find Bob PPP session before remove: %v", err)
		}
		revision := policy.Revision + 1
		request := &nodev1.RemoveInboundUserRequest{OperationId: "native-l2tp-remove-bob", InboundTag: inbound.Tag, Email: fmt.Sprintf("%s%d:%d", l2TPRemoveUserPrefix, bob.UserID, revision)}
		response, err := server.RemoveUser(context.Background(), request)
		if err != nil || response == nil || !response.GetAccepted() {
			t.Fatalf("real NodeAgent RemoveUser RPC handler failed: %v", err)
		}
		afterRemove, err := server.currentRuntimePolicy()
		if err != nil || afterRemove == nil || afterRemove.Revision != policy.Revision+1 {
			t.Fatalf("RemoveUser did not persist exactly one policy revision: before=%d after=%v err=%v", policy.Revision, afterRemove, err)
		}
		retryResponse, err := server.RemoveUser(context.Background(), request)
		if err != nil || retryResponse == nil || !retryResponse.GetAccepted() {
			t.Fatalf("same-operation RemoveUser retry was not idempotent: %v", err)
		}
		assertL2TPNativePolicyRevision(t, server, afterRemove.Revision)
		assertL2TPNativeCredentialAbsent(t, runtimeRoot, *bob)
		if err := waitL2TPNativeProcessGone(before.Process, 5*time.Second); err != nil {
			t.Fatalf("target Bob pppd process still exists after RemoveUser: %v", err)
		}
		if _, err := pppOfflineReadIdentity(before.Interface); err == nil {
			t.Fatalf("target Bob PPP interface %s remained after removal", before.Interface)
		}
		assertRuntimeUnchanged()
	case "rollback":
		charlie := l2TPRuntimeUser{UserID: 9, Username: "native-l2tp-rollback", VPNUsername: "native-l2tp-rollback", Password: "native-l2tp-rollback-secret", IPv4Address: "10.67.0.4", Status: "active"}
		files := []string{
			filepath.Join(runtimeRoot, "chap-secrets"), filepath.Join(runtimeRoot, "session-helper.json"),
			filepath.Join(runtimeRoot, "usage-helper.json"), l2TPCHAPSecretsPath,
		}
		before := make(map[string][]byte, len(files))
		for _, path := range files {
			before[path], err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		policyPath := server.runtimePolicyPath()
		backupPath := policyPath + ".native-e2e-backup"
		if _, err := os.Stat(backupPath); err == nil {
			t.Fatalf("stale native E2E backup exists at %s", backupPath)
		}
		if err := os.Rename(policyPath, backupPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(policyPath, 0700); err != nil {
			_ = os.Rename(backupPath, policyPath)
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(policyPath)
			_ = os.Rename(backupPath, policyPath)
		})
		_, err := server.AddUser(context.Background(), &nodev1.InboundUserRequest{
			OperationId: "native-l2tp-persistence-failure", InboundTag: inbound.Tag,
			User: nativeL2TPMutationUser(charlie, "add", policy.Revision+1),
		})
		if err == nil || !strings.Contains(err.Error(), "persist L2TP user policy") {
			t.Fatalf("real runtime policy file failure was not reported: %v", err)
		}
		for _, path := range files {
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before[path]) {
				t.Fatalf("runtime file rollback failed for %s: read err=%v", filepath.Base(path), readErr)
			}
		}
		assertRuntimeUnchanged()
	default:
		t.Fatalf("unsupported native L2TP mutation action %q", action)
	}
}

func nativeL2TPMutationServer(t *testing.T, dataDir string) (*Server, *runtimePolicySnapshot, l2TPRuntimeInbound, string) {
	t.Helper()
	tag := "native-l2tp"
	runtimeRoot := filepath.Join(dataDir, "l2tp", l2TPRuntimeDirName(tag))
	var helper nativeSessionHelperConfig
	raw, err := os.ReadFile(filepath.Join(runtimeRoot, "session-helper.json"))
	if err != nil {
		t.Fatalf("read live L2TP session helper: %v", err)
	}
	if err := json.Unmarshal(raw, &helper); err != nil {
		t.Fatal(err)
	}
	if helper.Protocol != "l2tp" || helper.InboundTag != tag {
		t.Fatalf("unexpected live L2TP helper identity: %+v", helper)
	}
	inbound := l2TPRuntimeInbound{Tag: tag, Port: defaultL2TPPort, Settings: map[string]any{
		"ipsec_psk": "native-l2tp-psk", "ipv4_pool_cidr": "10.67.0.0/24", "mtu": 1200, "mru": 1200,
	}}
	s := New(Config{DataDir: dataDir})
	policy, err := s.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil {
		inbound.Users = []l2TPRuntimeUser{{UserID: 7, Username: "native-l2tp", VPNUsername: "native-l2tp", Password: "native-l2tp-secret", IPv4Address: "10.67.0.2", Status: "active", UsageCoefficient: 1.5, InboundCoefficient: 2}}
		payload := nativeRuntimePayload{Target: "native-e2e", SessionCallback: helper.Callback, L2TPInbounds: []l2TPRuntimeInbound{inbound}}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: string(raw), DesiredRevision: 1}); err != nil {
			t.Fatal(err)
		}
		policy, err = s.currentRuntimePolicy()
		if err != nil || policy == nil {
			t.Fatalf("load seeded native L2TP runtime policy: policy=%v err=%v", policy, err)
		}
	} else {
		payload, err := parseNativeRuntimePayload(policy.NativeJSON)
		if err != nil {
			t.Fatal(err)
		}
		for _, existing := range payload.L2TPInbounds {
			if existing.Tag == tag {
				inbound = existing
				break
			}
		}
		if len(inbound.Users) == 0 {
			t.Fatalf("durable native L2TP policy has no users: revision=%d", policy.Revision)
		}
	}
	files := l2TPRuntimeFiles{
		IPSecConfig: filepath.Join(runtimeRoot, "ipsec.conf"), IPSecSecrets: filepath.Join(runtimeRoot, "ipsec.secrets"),
		XL2TPConfig: filepath.Join(runtimeRoot, "xl2tpd.conf"), PPPOptions: filepath.Join(runtimeRoot, "ppp-options"),
		CHAPSecrets: filepath.Join(runtimeRoot, "chap-secrets"), IPUpScript: filepath.Join(runtimeRoot, "ip-up.sh"),
		IPDownScript: filepath.Join(runtimeRoot, "ip-down.sh"), SessionConfig: filepath.Join(runtimeRoot, "session-helper.json"),
		UsageConfig: filepath.Join(runtimeRoot, "usage-helper.json"),
	}
	helperBinary := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_HELPER_BINARY"))
	if helperBinary == "" {
		helperBinary, err = pppSessionHelperExecutable()
		if err != nil {
			t.Fatal(err)
		}
	}
	helperBinary, err = filepath.Abs(helperBinary)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := l2TPRuntimeIdentity(files, helperBinary)
	if err != nil {
		t.Fatalf("compute active L2TP runtime identity: %v", err)
	}
	s.l2TPRuntimes[tag] = &l2TPProcess{tag: tag, identity: identity}
	return s, policy, inbound, runtimeRoot
}

func nativeL2TPMutationUser(user l2TPRuntimeUser, operation string, revision uint64) *nodev1.InboundUser {
	fields := map[string]string{
		l2TPMutationUserIDField: strconv.FormatInt(user.UserID, 10),
		l2TPMutationOperation:   operation,
		l2TPMutationRevision:    strconv.FormatUint(revision, 10),
	}
	if operation != "remove" {
		raw, _ := json.Marshal(user)
		fields[l2TPMutationUserJSON] = string(raw)
	}
	return &nodev1.InboundUser{Protocol: "l2tp", Fields: fields}
}

func nativeL2TPActiveUser(runtimeRoot string, userID int64) (pppOfflineSession, error) {
	paths, err := filepath.Glob(filepath.Join(runtimeRoot, "ppp-accounting", "active", "ppp[0-9]*-*.json"))
	if err != nil {
		return pppOfflineSession{}, err
	}
	var found pppOfflineSession
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return pppOfflineSession{}, err
		}
		var session pppOfflineSession
		if err := json.Unmarshal(raw, &session); err != nil {
			return pppOfflineSession{}, err
		}
		if session.UserID != userID || session.InboundTag != "native-l2tp" {
			continue
		}
		if !pppOfflineProcessIsLive(session.Process) {
			// Stop hooks retain active metadata so they can be retried after a
			// lost callback ACK. Ignore records whose exact pppd process exited.
			continue
		}
		if found.ID != "" {
			return pppOfflineSession{}, fmt.Errorf("multiple live sessions for user %d", userID)
		}
		if identity, err := pppOfflineReadIdentity(session.Interface); err != nil || identity != session.Identity {
			return pppOfflineSession{}, fmt.Errorf("session %s PPP interface identity changed: record=%s live=%s err=%v", session.ID, session.Identity, identity, err)
		}
		found = session
	}
	if found.ID == "" {
		return pppOfflineSession{}, os.ErrNotExist
	}
	return found, nil
}

func assertL2TPNativeCredential(t *testing.T, runtimeRoot string, user l2TPRuntimeUser) {
	t.Helper()
	chap, err := os.ReadFile(l2TPCHAPSecretsPath)
	if err != nil || !strings.Contains(string(chap), l2TPConfigQuote(user.Password)) || !strings.Contains(string(chap), l2TPConfigQuote(l2TPRuntimeUsername(user))) {
		t.Fatalf("native CHAP file does not contain target credential: err=%v", err)
	}
	raw, err := os.ReadFile(filepath.Join(runtimeRoot, "session-helper.json"))
	if err != nil {
		t.Fatal(err)
	}
	var helper nativeSessionHelperConfig
	if err := json.Unmarshal(raw, &helper); err != nil {
		t.Fatal(err)
	}
	if helper.Transitioning || helper.Users[l2TPRuntimeUsername(user)] != user.UserID {
		t.Fatalf("native session helper did not activate target user: %+v", helper)
	}
}

func assertL2TPNativeCredentialAbsent(t *testing.T, runtimeRoot string, user l2TPRuntimeUser) {
	t.Helper()
	for _, path := range []string{l2TPCHAPSecretsPath, filepath.Join(runtimeRoot, "chap-secrets")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), l2TPConfigQuote(user.Password)) || strings.Contains(string(raw), l2TPConfigQuote(l2TPRuntimeUsername(user))) {
			t.Fatalf("removed user's credential remains in %s", filepath.Base(path))
		}
	}
	raw, err := os.ReadFile(filepath.Join(runtimeRoot, "session-helper.json"))
	if err != nil {
		t.Fatal(err)
	}
	var helper nativeSessionHelperConfig
	if err := json.Unmarshal(raw, &helper); err != nil {
		t.Fatal(err)
	}
	if _, exists := helper.Users[l2TPRuntimeUsername(user)]; exists {
		t.Fatalf("removed user remains in native session policy: user_id=%d", user.UserID)
	}
}

func assertL2TPNativePolicyRevision(t *testing.T, server *Server, expected uint64) {
	t.Helper()
	policy, err := server.currentRuntimePolicy()
	if err != nil || policy == nil || policy.Revision != expected {
		t.Fatalf("operation retry changed durable policy revision: expected=%d actual=%v err=%v", expected, policy, err)
	}
}

func waitL2TPNativeProcessGone(identity string, timeout time.Duration) error {
	parts := strings.Split(identity, ":")
	if len(parts) != 3 {
		return fmt.Errorf("invalid PPP process identity %q", identity)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := pppOfflineReadLiveProcess(parts[1])
		if os.IsNotExist(err) || (err == nil && current != identity) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("verify PPP process identity: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("PPP process %s remained live after %s", identity, timeout)
}
