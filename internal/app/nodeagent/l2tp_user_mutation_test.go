package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestL2TPTargetedMutationUpdatesPolicyWithoutRestartAndIsIdempotent(t *testing.T) {
	h := newL2TPMutationFixture(t)
	added := l2TPRuntimeUser{UserID: 8, Username: "bob", VPNUsername: "bob", Password: "pw-bob", IPv4Address: "10.67.0.8", Status: "active"}
	request := h.user(8, "add", &added, 2)
	rpcRequest := &nodev1.InboundUserRequest{OperationId: "operation-add-bob", InboundTag: "l2tp-main", User: request}
	response, err := h.server.AddUser(context.Background(), rpcRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || !response.GetAccepted() || response.GetOperationId() != rpcRequest.GetOperationId() {
		t.Fatalf("AddUser returned invalid acknowledgement: %+v", response)
	}
	if _, err := h.server.AddUser(context.Background(), rpcRequest); err != nil {
		t.Fatalf("same operation retry was not idempotent: %v", err)
	}
	conflict := added
	conflict.Password = "different"
	conflictingRequest := &nodev1.InboundUserRequest{OperationId: "operation-add-bob", InboundTag: "l2tp-main", User: h.user(8, "add", &conflict, 2)}
	if _, err := h.server.AddUser(context.Background(), conflictingRequest); err == nil || !strings.Contains(err.Error(), "reused with a different mutation") {
		t.Fatalf("conflicting operation id was not rejected: %v", err)
	}
	policy, err := h.server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.L2TPInbounds) != 1 || len(payload.L2TPInbounds[0].Users) != 2 {
		t.Fatalf("targeted policy did not preserve the existing user: %+v", payload.L2TPInbounds)
	}
	chap, err := os.ReadFile(filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "chap-secrets"))
	if err != nil || !strings.Contains(string(chap), `"bob"`) {
		t.Fatalf("target user was not immediately written to CHAP state: %q err=%v", chap, err)
	}
	globalCHAP, err := os.ReadFile(l2TPCHAPSecretsPath)
	if err != nil || !strings.Contains(string(globalCHAP), `"bob"`) {
		t.Fatalf("target user was not immediately written to the managed CHAP block: %q err=%v", globalCHAP, err)
	}
	helperRaw, err := os.ReadFile(filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "session-helper.json"))
	if err != nil {
		t.Fatal(err)
	}
	var helper nativeSessionHelperConfig
	if err := json.Unmarshal(helperRaw, &helper); err != nil {
		t.Fatal(err)
	}
	if helper.Transitioning || helper.Users["bob"] != 8 {
		t.Fatalf("final helper policy is not active: %+v", helper)
	}
	if len(h.terminated) != 0 {
		t.Fatalf("Add disconnected an existing user: terminations=%v", h.terminated)
	}
}

func TestL2TPTargetedRemoveTerminatesOnlyTargetAndPersistenceFailureRollsBack(t *testing.T) {
	t.Run("stale exited session record is not terminated", func(t *testing.T) {
		h := newL2TPMutationFixture(t)
		root := filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"))
		session := pppOfflineSession{ID: "exited-target", UserID: 7, InboundTag: "l2tp-main", Interface: "ppp0", Process: "fixture-boot:101:1"}
		if err := offlineDurableJSON(pppOfflineSessionActivePath(root, session.Interface, session.Process), session); err != nil {
			t.Fatal(err)
		}
		previousReadProcess := pppOfflineReadLiveProcess
		pppOfflineReadLiveProcess = func(string) (string, error) { return "", os.ErrNotExist }
		t.Cleanup(func() { pppOfflineReadLiveProcess = previousReadProcess })
		request := &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-exited", InboundTag: "l2tp-main", Email: l2TPRemoveUserPrefix + "7:2"}
		if _, err := h.server.RemoveUser(context.Background(), request); err != nil {
			t.Fatalf("remove should ignore an exited process retained in active metadata: %v", err)
		}
		if len(h.terminated) != 0 {
			t.Fatalf("stale exited session was sent a termination signal: %v", h.terminated)
		}
	})

	t.Run("process exit racing pidfd open is idempotent", func(t *testing.T) {
		h := newL2TPMutationFixture(t)
		root := filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"))
		session := pppOfflineSession{ID: "racing-target", UserID: 7, InboundTag: "l2tp-main", Interface: "ppp0", Process: "fixture-boot:101:1"}
		if err := offlineDurableJSON(pppOfflineSessionActivePath(root, session.Interface, session.Process), session); err != nil {
			t.Fatal(err)
		}
		exited := false
		previousReadProcess := pppOfflineReadLiveProcess
		pppOfflineReadLiveProcess = func(string) (string, error) {
			if exited {
				return "", os.ErrNotExist
			}
			return session.Process, nil
		}
		t.Cleanup(func() { pppOfflineReadLiveProcess = previousReadProcess })
		previousTerminate := l2TPMutationTerminateSession
		l2TPMutationTerminateSession = func(context.Context, string) error {
			exited = true
			return os.ErrNotExist
		}
		t.Cleanup(func() { l2TPMutationTerminateSession = previousTerminate })
		request := &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-exit-race", InboundTag: "l2tp-main", Email: l2TPRemoveUserPrefix + "7:2"}
		if _, err := h.server.RemoveUser(context.Background(), request); err != nil {
			t.Fatalf("remove should accept a confirmed process exit racing pidfd open: %v", err)
		}
	})

	t.Run("remove only target session", func(t *testing.T) {
		h := newL2TPMutationFixture(t)
		activeDir := filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "ppp-accounting", "active")
		for _, session := range []pppOfflineSession{
			{ID: "target", UserID: 7, InboundTag: "l2tp-main", Interface: "ppp0", Process: "boot:101:1"},
			{ID: "other", UserID: 9, InboundTag: "l2tp-main", Interface: "ppp1", Process: "boot:102:1"},
		} {
			if err := offlineDurableJSON(pppOfflineSessionActivePath(filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main")), session.Interface, session.Process), session); err != nil {
				t.Fatal(err)
			}
		}
		previousReadProcess := pppOfflineReadLiveProcess
		pppOfflineReadLiveProcess = func(pid string) (string, error) {
			if pid == "101" {
				return "boot:101:1", nil
			}
			return "", os.ErrNotExist
		}
		t.Cleanup(func() { pppOfflineReadLiveProcess = previousReadProcess })
		_, err := h.server.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-alice", InboundTag: "l2tp-main", Email: l2TPRemoveUserPrefix + "7:2"})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.terminated) != 1 || h.terminated[0] != "boot:101:1" {
			t.Fatalf("remove did not terminate only the target session: %v (active dir %s)", h.terminated, activeDir)
		}
	})

	t.Run("failed target revoke is not acknowledged by a replay", func(t *testing.T) {
		h := newL2TPMutationFixture(t)
		root := filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"))
		session := pppOfflineSession{ID: "target", UserID: 7, InboundTag: "l2tp-main", Interface: "ppp0", Process: "fixture-boot:101:1"}
		if err := offlineDurableJSON(pppOfflineSessionActivePath(root, session.Interface, session.Process), session); err != nil {
			t.Fatal(err)
		}
		previousReadProcess := pppOfflineReadLiveProcess
		pppOfflineReadLiveProcess = func(string) (string, error) { return session.Process, nil }
		t.Cleanup(func() { pppOfflineReadLiveProcess = previousReadProcess })
		previousTerminate := l2TPMutationTerminateSession
		l2TPMutationTerminateSession = func(context.Context, string) error { return errors.New("injected process termination failure") }
		t.Cleanup(func() { l2TPMutationTerminateSession = previousTerminate })
		request := &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-retry", InboundTag: "l2tp-main", Email: l2TPRemoveUserPrefix + "7:2"}
		if _, err := h.server.RemoveUser(context.Background(), request); err == nil || !strings.Contains(err.Error(), "termination failure") {
			t.Fatalf("expected target termination failure, got %v", err)
		}
		if _, err := h.server.RemoveUser(context.Background(), request); err == nil || !strings.Contains(err.Error(), "still has a live or pending PPP session") {
			t.Fatalf("retry falsely acknowledged a still-live target session: %v", err)
		}
		pppOfflineReadLiveProcess = func(string) (string, error) { return "", os.ErrNotExist }
		if _, err := h.server.RemoveUser(context.Background(), request); err != nil {
			t.Fatalf("retry after target process exit should be acknowledged: %v", err)
		}
	})

	t.Run("persistence failure restores all files", func(t *testing.T) {
		h := newL2TPMutationFixture(t)
		paths := []string{
			filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "chap-secrets"),
			filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "session-helper.json"),
			filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"), "usage-helper.json"),
			l2TPCHAPSecretsPath,
		}
		before := make(map[string][]byte, len(paths))
		for _, path := range paths {
			before[path], _ = os.ReadFile(path)
		}
		previousWrite := runtimePolicyAtomicWrite
		runtimePolicyAtomicWrite = func(string, []byte) error { return errors.New("injected policy persistence failure") }
		t.Cleanup(func() { runtimePolicyAtomicWrite = previousWrite })
		added := l2TPRuntimeUser{UserID: 8, Username: "bob", VPNUsername: "bob", Password: "pw-bob", IPv4Address: "10.67.0.8", Status: "active"}
		_, err := h.server.AddUser(context.Background(), &nodev1.InboundUserRequest{OperationId: "operation-fail-persist", InboundTag: "l2tp-main", User: h.user(8, "add", &added, 2)})
		if err == nil || !strings.Contains(err.Error(), "injected policy persistence failure") {
			t.Fatalf("expected persistence failure, got %v", err)
		}
		for _, path := range paths {
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before[path]) {
				t.Fatalf("mutation did not restore %s: read err=%v before=%q after=%q", filepath.Base(path), readErr, before[path], after)
			}
		}
	})
}

func TestL2TPTargetedUpdateKeepsExistingPPPInterfaceAndChangesCredentialPolicy(t *testing.T) {
	h := newL2TPMutationFixture(t)
	root := filepath.Join(h.root, "l2tp", l2TPRuntimeDirName("l2tp-main"))
	processIdentity, err := offlineProcessIdentity(strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	session := pppOfflineSession{ID: "alice-live", UserID: 7, InboundTag: "l2tp-main", Interface: "ppp0", PeerIP: "10.67.0.7", ClientIP: "198.51.100.7", Identity: "fixture-ifindex-4", Process: processIdentity}
	activePath := pppOfflineSessionActivePath(root, session.Interface, session.Process)
	if err := offlineDurableJSON(activePath, session); err != nil {
		t.Fatal(err)
	}
	updated := l2TPRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "new-password", IPv4Address: "10.67.0.7", Status: "active"}
	_, err = h.server.UpdateUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "operation-update-alice", InboundTag: "l2tp-main", User: h.user(7, "update", &updated, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.terminated) != 0 {
		t.Fatalf("credential update interrupted the target PPP session: %v", h.terminated)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("existing PPP interface session metadata was lost: %v", err)
	}
	chap, err := os.ReadFile(l2TPCHAPSecretsPath)
	if err != nil || !strings.Contains(string(chap), `"new-password"`) {
		t.Fatalf("updated CHAP credential was not applied: %q err=%v", chap, err)
	}
	policy, err := h.server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil || payload.L2TPInbounds[0].Users[0].Password != "new-password" {
		t.Fatalf("updated desired state was not persisted: users=%+v err=%v", payload.L2TPInbounds[0].Users, err)
	}
	addressChanged := updated
	addressChanged.IPv4Address = "10.67.0.9"
	_, err = h.server.UpdateUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "operation-update-alice-address", InboundTag: "l2tp-main", User: h.user(7, "update", &addressChanged, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.terminated) != 1 || h.terminated[0] != session.Process {
		t.Fatalf("fixed-IP change did not revoke only the target's old-address session: %v", h.terminated)
	}
}

type l2TPMutationTestFixture struct {
	server     *Server
	root       string
	terminated []string
	user       func(int64, string, *l2TPRuntimeUser, uint64) *nodev1.InboundUser
}

func newL2TPMutationFixture(t *testing.T) *l2TPMutationTestFixture {
	t.Helper()
	root := t.TempDir()
	previousCHAP := l2TPCHAPSecretsPath
	previousCheckpoint := l2TPMutationCheckpoint
	previousActive := l2TPMutationRuntimeActive
	previousTerminate := l2TPMutationTerminateSession
	l2TPCHAPSecretsPath = filepath.Join(root, "etc", "ppp", "chap-secrets")
	l2TPMutationCheckpoint = func(*Server, context.Context) error { return nil }
	l2TPMutationRuntimeActive = func() bool { return true }
	fixture := &l2TPMutationTestFixture{root: root}
	l2TPMutationTerminateSession = func(_ context.Context, identity string) error {
		fixture.terminated = append(fixture.terminated, identity)
		return nil
	}
	t.Cleanup(func() {
		l2TPCHAPSecretsPath = previousCHAP
		l2TPMutationCheckpoint = previousCheckpoint
		l2TPMutationRuntimeActive = previousActive
		l2TPMutationTerminateSession = previousTerminate
	})
	server := New(Config{DataDir: root})
	fixture.server = server
	initialUser := l2TPRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "pw-alice", IPv4Address: "10.67.0.7", Status: "active"}
	inbound := l2TPRuntimeInbound{Tag: "l2tp-main", Port: defaultL2TPPort, Settings: map[string]any{"ipsec_psk": "test-psk", "ipv4_pool_cidr": "10.67.0.0/16"}, Users: []l2TPRuntimeUser{initialUser}}
	files, err := server.prepareL2TPInbound(inbound, nativeRuntimeSessionCallback{URL: "http://127.0.0.1/callback", Token: "token", NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	global := replaceL2TPManagedBlock(nil, l2TPCHAPBlockStart, l2TPCHAPBlockEnd, renderL2TPCHAPSecrets(inbound.Users))
	if err := os.MkdirAll(filepath.Dir(l2TPCHAPSecretsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(l2TPCHAPSecretsPath, global, 0600); err != nil {
		t.Fatal(err)
	}
	server.l2TPRuntimes["l2tp-main"] = &l2TPProcess{tag: "l2tp-main", identity: files.Identity}
	payload := nativeRuntimePayload{Target: "node:1", SessionCallback: nativeRuntimeSessionCallback{URL: "http://127.0.0.1/callback", Token: "token", NodeID: 1}, L2TPInbounds: []l2TPRuntimeInbound{inbound}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: string(raw), DesiredRevision: 1}); err != nil {
		t.Fatal(err)
	}
	fixture.user = func(id int64, operation string, desired *l2TPRuntimeUser, revision uint64) *nodev1.InboundUser {
		fields := map[string]string{l2TPMutationUserIDField: strconv.FormatInt(id, 10), l2TPMutationOperation: operation, l2TPMutationRevision: strconv.FormatUint(revision, 10)}
		if desired != nil {
			encoded, marshalErr := json.Marshal(desired)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			fields[l2TPMutationUserJSON] = string(encoded)
		}
		return &nodev1.InboundUser{Protocol: "l2tp", Fields: fields}
	}
	return fixture
}
