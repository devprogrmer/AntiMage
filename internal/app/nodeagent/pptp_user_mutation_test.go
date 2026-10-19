package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestPPTPTargetedMutationUpdatesPolicyWithoutRestartAndIsIdempotent(t *testing.T) {
	h := newPPTPMutationFixture(t)
	added := pptpRuntimeUser{UserID: 8, Username: "bob", VPNUsername: "bob", Password: "pw-bob", IPv4Address: "10.68.0.8", Status: "active"}
	request := h.user(8, "add", &added, 2)
	rpcRequest := &nodev1.InboundUserRequest{OperationId: "operation-add-bob", InboundTag: "pptp-main", User: request}
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
	conflictingRequest := &nodev1.InboundUserRequest{OperationId: "operation-add-bob", InboundTag: "pptp-main", User: h.user(8, "add", &conflict, 2)}
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
	if len(payload.PPTPInbounds) != 1 || len(payload.PPTPInbounds[0].Users) != 2 {
		t.Fatalf("targeted policy did not preserve the existing user: %+v", payload.PPTPInbounds)
	}
	config, err := os.ReadFile(filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"), "pptpd.conf"))
	if err != nil || len(config) == 0 {
		t.Fatalf("pptpd config was not staged for the target inbound: %q err=%v", config, err)
	}
	globalCHAP, err := os.ReadFile(pptpCHAPSecretsPath)
	if err != nil || !strings.Contains(string(globalCHAP), `"bob"`) {
		t.Fatalf("target user was not immediately written to the managed CHAP block: %q err=%v", globalCHAP, err)
	}
	helperRaw, err := os.ReadFile(filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"), "session-helper.json"))
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
	if h.stopped {
		t.Fatalf("PPTP mutation stopped the pptpd supervisor")
	}
}

func TestPPTPTargetedUpdateKeepsExistingPPPSessionAndChangesCredentialPolicy(t *testing.T) {
	h := newPPTPMutationFixture(t)
	root := filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"))
	processIdentity, err := offlineProcessIdentity(strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	session := pppOfflineSession{ID: "alice-live", UserID: 7, InboundTag: "pptp-main", Interface: "ppp0", PeerIP: "10.68.0.7", ClientIP: "198.51.100.7", Identity: "fixture-ifindex-4", Process: processIdentity}
	activePath := pppOfflineSessionActivePath(root, session.Interface, session.Process)
	if err := offlineDurableJSON(activePath, session); err != nil {
		t.Fatal(err)
	}
	updated := pptpRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "new-password", IPv4Address: "10.68.0.7", Status: "active"}
	_, err = h.server.UpdateUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "operation-update-alice", InboundTag: "pptp-main", User: h.user(7, "update", &updated, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.terminated) != 0 {
		t.Fatalf("credential update interrupted the target PPP session: %v", h.terminated)
	}
	if h.stopped {
		t.Fatalf("PPTP credential update stopped the pptpd supervisor")
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("existing PPP interface session metadata was lost: %v", err)
	}
	chap, err := os.ReadFile(pptpCHAPSecretsPath)
	if err != nil || !strings.Contains(string(chap), `"new-password"`) {
		t.Fatalf("updated CHAP credential was not applied: %q err=%v", chap, err)
	}
	policy, err := h.server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseNativeRuntimePayload(policy.NativeJSON)
	if err != nil || payload.PPTPInbounds[0].Users[0].Password != "new-password" {
		t.Fatalf("updated desired state was not persisted: users=%+v err=%v", payload.PPTPInbounds[0].Users, err)
	}
	addressChanged := updated
	addressChanged.IPv4Address = "10.68.0.9"
	_, err = h.server.UpdateUser(context.Background(), &nodev1.InboundUserRequest{
		OperationId: "operation-update-alice-address", InboundTag: "pptp-main", User: h.user(7, "update", &addressChanged, 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.terminated) != 1 || h.terminated[0] != session.Process {
		t.Fatalf("fixed-IP change did not revoke only the target's old-address session: %v", h.terminated)
	}
}

func TestPPTPTargetedRemoveTerminatesOnlyTargetAndPersistenceFailureRollsBack(t *testing.T) {
	t.Run("stale exited session record is not terminated", func(t *testing.T) {
		h := newPPTPMutationFixture(t)
		root := filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"))
		session := pppOfflineSession{ID: "exited-target", UserID: 7, InboundTag: "pptp-main", Interface: "ppp0", Process: "fixture-boot:101:1"}
		if err := offlineDurableJSON(pppOfflineSessionActivePath(root, session.Interface, session.Process), session); err != nil {
			t.Fatal(err)
		}
		previousReadProcess := pppOfflineReadLiveProcess
		pppOfflineReadLiveProcess = func(string) (string, error) { return "", os.ErrNotExist }
		t.Cleanup(func() { pppOfflineReadLiveProcess = previousReadProcess })
		request := &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-exited", InboundTag: "pptp-main", Email: pptpRemoveUserPrefix + "7:2"}
		if _, err := h.server.RemoveUser(context.Background(), request); err != nil {
			t.Fatalf("remove should ignore an exited process retained in active metadata: %v", err)
		}
		if len(h.terminated) != 0 {
			t.Fatalf("stale exited session was sent a termination signal: %v", h.terminated)
		}
	})

	t.Run("remove only target session", func(t *testing.T) {
		h := newPPTPMutationFixture(t)
		root := filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"))
		for _, session := range []pppOfflineSession{
			{ID: "target", UserID: 7, InboundTag: "pptp-main", Interface: "ppp0", Process: "boot:101:1"},
			{ID: "other", UserID: 9, InboundTag: "pptp-main", Interface: "ppp1", Process: "boot:102:1"},
		} {
			if err := offlineDurableJSON(pppOfflineSessionActivePath(root, session.Interface, session.Process), session); err != nil {
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
		_, err := h.server.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{OperationId: "operation-remove-alice", InboundTag: "pptp-main", Email: pptpRemoveUserPrefix + "7:2"})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.terminated) != 1 || h.terminated[0] != "boot:101:1" {
			t.Fatalf("remove did not terminate exactly the target session: %v", h.terminated)
		}
	})

	t.Run("persistence failure rolls the staged files back", func(t *testing.T) {
		h := newPPTPMutationFixture(t)
		root := filepath.Join(h.root, "pptp", pptpRuntimeDirName("pptp-main"))
		before, err := os.ReadFile(filepath.Join(root, "session-helper.json"))
		if err != nil {
			t.Fatal(err)
		}
		previousCommit := pptpMutationCommitOverride
		pptpMutationCommitOverride = func(*Server, string, string, string, uint64) error {
			return os.ErrPermission
		}
		t.Cleanup(func() { pptpMutationCommitOverride = previousCommit })
		if _, err := h.server.AddUser(context.Background(), &nodev1.InboundUserRequest{
			OperationId: "operation-add-rollback", InboundTag: "pptp-main",
			User: h.user(9, "add", &pptpRuntimeUser{UserID: 9, Username: "carol", VPNUsername: "carol", Password: "pw", IPv4Address: "10.68.0.9", Status: "active"}, 2),
		}); err == nil {
			t.Fatal("expected persistence failure to surface")
		}
		after, err := os.ReadFile(filepath.Join(root, "session-helper.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("staged session policy was not rolled back after persistence failure")
		}
		globalCHAP, err := os.ReadFile(pptpCHAPSecretsPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(globalCHAP), `"carol"`) {
			t.Fatalf("target user leaked into managed CHAP after rollback: %q", globalCHAP)
		}
	})
}

func TestPPTPRemoveUserIdentityRejectsMalformedInput(t *testing.T) {
	for _, value := range []string{"pptp-user:", "pptp-user:0:1", "pptp-user:7", "pptp-user:7:0", "pptp-user:x:1"} {
		if _, _, ok := pptpRemoveUserIdentity(value); ok {
			t.Fatalf("malformed PPTP remove identity %q was accepted", value)
		}
	}
	userID, revision, ok := pptpRemoveUserIdentity("pptp-user:7:2")
	if !ok || userID != 7 || revision != 2 {
		t.Fatalf("valid PPTP remove identity was not parsed: id=%d rev=%d ok=%v", userID, revision, ok)
	}
}

type pptpMutationTestFixture struct {
	server     *Server
	root       string
	terminated []string
	stopped    bool
	user       func(int64, string, *pptpRuntimeUser, uint64) *nodev1.InboundUser
}

func newPPTPMutationFixture(t *testing.T) *pptpMutationTestFixture {
	t.Helper()
	root := t.TempDir()
	previousCHAP := pptpCHAPSecretsPath
	previousCheckpoint := pptpMutationCheckpoint
	previousActive := pptpMutationRuntimeActive
	previousTerminate := pptpMutationTerminateSession
	previousCommit := pptpMutationCommitOverride
	pptpCHAPSecretsPath = filepath.Join(root, "etc", "ppp", "chap-secrets")
	pptpMutationCheckpoint = func(*Server, context.Context) error { return nil }
	pptpMutationRuntimeActive = func() bool { return true }
	pptpMutationCommitOverride = nil
	fixture := &pptpMutationTestFixture{root: root}
	pptpMutationTerminateSession = func(_ context.Context, identity string) error {
		fixture.terminated = append(fixture.terminated, identity)
		return nil
	}
	t.Cleanup(func() {
		pptpCHAPSecretsPath = previousCHAP
		pptpMutationCheckpoint = previousCheckpoint
		pptpMutationRuntimeActive = previousActive
		pptpMutationTerminateSession = previousTerminate
		pptpMutationCommitOverride = previousCommit
	})
	server := New(Config{DataDir: root})
	fixture.server = server
	initialUser := pptpRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "pw-alice", IPv4Address: "10.68.0.7", Status: "active"}
	inbound := pptpRuntimeInbound{Tag: "pptp-main", Port: defaultPPTPPort, Settings: map[string]any{"ipv4_pool_cidr": "10.68.0.0/24"}, Users: []pptpRuntimeUser{initialUser}}
	if _, err := server.preparePPTPInbound(inbound, nativeRuntimeSessionCallback{URL: "http://127.0.0.1/callback", Token: "token", NodeID: 1}); err != nil {
		t.Fatal(err)
	}
	global := replaceL2TPManagedBlock(nil, pptpCHAPBlockStart, pptpCHAPBlockEnd, dedupePPTPCHAPSecrets(renderPPTPCHAPSecrets(inbound.Users)))
	if err := os.MkdirAll(filepath.Dir(pptpCHAPSecretsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(pptpCHAPSecretsPath, global, 0600); err != nil {
		t.Fatal(err)
	}
	server.pptpRuntimes["pptp-main"] = &pptpProcess{done: make(chan struct{})}
	payload := nativeRuntimePayload{Target: "node:1", SessionCallback: nativeRuntimeSessionCallback{URL: "http://127.0.0.1/callback", Token: "token", NodeID: 1}, PPTPInbounds: []pptpRuntimeInbound{inbound}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: string(raw), DesiredRevision: 1}); err != nil {
		t.Fatal(err)
	}
	fixture.user = func(id int64, operation string, desired *pptpRuntimeUser, revision uint64) *nodev1.InboundUser {
		fields := map[string]string{pptpMutationUserIDField: strconv.FormatInt(id, 10), pptpMutationOperation: operation, pptpMutationRevision: strconv.FormatUint(revision, 10)}
		if desired != nil {
			encoded, marshalErr := json.Marshal(desired)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			fields[pptpMutationUserJSON] = string(encoded)
		}
		return &nodev1.InboundUser{Protocol: "pptp", Fields: fields}
	}
	return fixture
}
