package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func anyConnectMutationRequest(operationID, operation string, revision uint64, desired anyConnectRuntimeUser) *nodev1.InboundUserRequest {
	raw, _ := json.Marshal(desired)
	return &nodev1.InboundUserRequest{OperationId: operationID, InboundTag: "ac-test", User: &nodev1.InboundUser{Protocol: "anyconnect", Fields: map[string]string{
		anyConnectMutationUserIDField:   fmt.Sprint(desired.UserID),
		anyConnectMutationOpField:       operation,
		anyConnectMutationRevisionField: fmt.Sprint(revision),
		anyConnectMutationUserField:     string(raw),
	}}}
}

type anyConnectMutationFixture struct {
	server     *Server
	inbound    anyConnectRuntimeInbound
	prepared   preparedAnyConnectRuntime
	starts     *int
	disconnect *atomic.Int32
}

// setupAnyConnectMutationFixture wires the AnyConnect mutation path onto the
// in-process test helpers so targeted RPCs exercise real file publishing and
// process bookkeeping without needing an ocserv binary.
func setupAnyConnectMutationFixture(t *testing.T, users []anyConnectRuntimeUser) *anyConnectMutationFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("AnyConnect process lifecycle fixture requires Unix process semantics")
	}
	oldCommand, oldCommandContext, oldLookPath, oldGrace := anyConnectCommand, anyConnectCommandContext, anyConnectLookPath, anyConnectStartupGrace
	oldOcctl, oldOcctlLookPath := anyConnectOcctlCommandContext, anyConnectOcctlLookPath
	oldCheckpoint := anyConnectMutationCheckpoint
	anyConnectCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestAnyConnectAnyHelper", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_ANY_HELPER=1")
		return cmd
	}
	anyConnectLookPath = func(string) (string, error) { return os.Args[0], nil }
	anyConnectStartupGrace = 10 * time.Millisecond
	starts := 0
	anyConnectCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		starts++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRuntimeHelperProcess", "--", "ocserv")
		cmd.Env = append(os.Environ(), "GO_WANT_RUNTIME_HELPER_PROCESS=1")
		return cmd
	}
	anyConnectMutationCheckpoint = func(*Server, context.Context) error { return nil }
	disconnects := &atomic.Int32{}
	anyConnectOcctlLookPath = func(string) (string, error) { return os.Args[0], nil }
	anyConnectOcctlCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		if len(args) != 5 || args[0] != "-s" || args[2] != "disconnect" || args[3] != "user" || args[4] != "bob" {
			t.Errorf("unexpected occtl invocation: %v", args)
		}
		disconnects.Add(1)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestAnyConnectQuickExitHelper", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_QUICK_EXIT=1")
		return cmd
	}
	t.Cleanup(func() {
		anyConnectCommand, anyConnectCommandContext, anyConnectLookPath, anyConnectStartupGrace = oldCommand, oldCommandContext, oldLookPath, oldGrace
		anyConnectOcctlCommandContext, anyConnectOcctlLookPath = oldOcctl, oldOcctlLookPath
		anyConnectMutationCheckpoint = oldCheckpoint
	})

	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllAnyConnectRuntimes)
	inbound := anyConnectRuntimeInbound{
		Tag: "ac-test", Port: 443,
		Settings: map[string]any{"ipv4_pool_cidr": "10.73.0.0/24", "server_certificate": "CERT", "server_key": "KEY", "accounting_enabled": false},
		Users:    users,
	}
	prepared, err := server.prepareAnyConnectInbound(inbound, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.startAnyConnectInbound(prepared); err != nil {
		t.Fatal(err)
	}
	native, err := json.Marshal(nativeRuntimePayload{AnyConnectInbounds: []anyConnectRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(native), DesiredRevision: 10}); err != nil {
		t.Fatal(err)
	}
	return &anyConnectMutationFixture{server: server, inbound: inbound, prepared: prepared, starts: &starts, disconnect: disconnects}
}

func TestAnyConnectTargetedRPCAddUpdateRemoveKeepsManagedProcessAndReplaysACK(t *testing.T) {
	fixture := setupAnyConnectMutationFixture(t, []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active"}})
	server := fixture.server
	server.mu.Lock()
	runtimeBefore := server.anyConnectRuntimes[fixture.inbound.Tag]
	pidBefore := runtimeBefore.cmd.Process.Pid
	server.mu.Unlock()

	add := anyConnectMutationRequest("add-11-ac-test", "add", 11, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-v1", Status: "active"})
	if response, err := server.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("AddUser response=%v err=%v", response, err)
	}
	update := anyConnectMutationRequest("update-12-ac-test", "update", 12, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-v2", Status: "active"})
	if response, err := server.UpdateUser(context.Background(), update); err != nil || !response.GetAccepted() {
		t.Fatalf("UpdateUser response=%v err=%v", response, err)
	}
	remove := &nodev1.RemoveInboundUserRequest{OperationId: "remove-13-ac-test", InboundTag: fixture.inbound.Tag,
		Email: fmt.Sprintf("%s2:13:%s", anyConnectRemoveUserPrefix, "Ym9i")}
	if response, err := server.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("RemoveUser response=%v err=%v", response, err)
	}
	remove.Email = fmt.Sprintf("%s2:14:%s", anyConnectRemoveUserPrefix, "Ym9i")
	if response, err := server.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("lost-ACK retry response=%v err=%v", response, err)
	}
	server.mu.Lock()
	runtimeAfter := server.anyConnectRuntimes[fixture.inbound.Tag]
	server.mu.Unlock()
	if *fixture.starts != 1 || runtimeAfter != runtimeBefore || runtimeAfter.cmd.Process.Pid != pidBefore {
		t.Fatalf("targeted mutations replaced ocserv process: starts=%d before=%d after=%d", *fixture.starts, pidBefore, runtimeAfter.cmd.Process.Pid)
	}
	if fixture.disconnect.Load() != 1 {
		t.Fatalf("remove ACK replay repeated session revoke: disconnects=%d", fixture.disconnect.Load())
	}
	current, err := server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseNativeRuntimePayload(current.NativeJSON)
	if err != nil || current.Revision != 14 || len(got.AnyConnectInbounds) != 1 || len(got.AnyConnectInbounds[0].Users) != 1 || got.AnyConnectInbounds[0].Users[0].UserID != 1 {
		t.Fatalf("targeted policy mutation changed unrelated user or retained removed user: %+v err=%v", got.AnyConnectInbounds, err)
	}
	passwords, err := os.ReadFile(fixture.prepared.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(passwords), "bob") {
		t.Fatalf("removed user credential file still includes bob: %s", passwords)
	}
	if _, err := os.Stat(filepath.Join(fixture.prepared.Files.UserConfigDir, "bob")); !os.IsNotExist(err) {
		t.Fatalf("removed user config still present: %v", err)
	}
}

func TestAnyConnectMutationDisconnectsDeniedUser(t *testing.T) {
	bob := anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-v1", Status: "active"}
	fixture := setupAnyConnectMutationFixture(t, []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active"}, bob})
	disabled := bob
	disabled.Status = "disabled"
	update := anyConnectMutationRequest("update-21-ac-test", "update", 11, disabled)
	if response, err := fixture.server.UpdateUser(context.Background(), update); err != nil || !response.GetAccepted() {
		t.Fatalf("UpdateUser response=%v err=%v", response, err)
	}
	if fixture.disconnect.Load() != 1 {
		t.Fatalf("disabling a live user did not revoke its session: disconnects=%d", fixture.disconnect.Load())
	}
}

func TestAnyConnectMutationConflictingOperationIDIsRejected(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	desired := anyConnectRuntimeUser{UserID: 7, Username: "alice", Password: "secret", Status: "active"}
	digest, err := anyConnectMutationDigest("ac-main", 7, "add", "alice", &desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recordAnyConnectMutationCompleted("add-123-ac-main", digest); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{cfg: Config{DataDir: s.cfg.DataDir}}
	completed, err := restarted.anyConnectMutationAlreadyCompleted("add-123-ac-main", digest)
	if err != nil || !completed {
		t.Fatalf("durable receipt completed=%v err=%v", completed, err)
	}
	other, _ := anyConnectMutationDigest("ac-main", 8, "add", "bob", &anyConnectRuntimeUser{UserID: 8, Username: "bob", Password: "x", Status: "active"})
	if _, err := restarted.anyConnectMutationAlreadyCompleted("add-123-ac-main", other); err == nil {
		t.Fatal("conflicting reuse of operation id was accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(restarted.anyConnectMutationResultPath("add-123-ac-main")))
	if err != nil || len(entries) != 1 {
		t.Fatalf("receipt directory entries=%d err=%v", len(entries), err)
	}
}

func TestAnyConnectMutationStaleRevisionIsRejected(t *testing.T) {
	fixture := setupAnyConnectMutationFixture(t, []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active"}})
	stale := anyConnectMutationRequest("add-5-ac-test", "add", 5, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-v1", Status: "active"})
	if _, err := fixture.server.AddUser(context.Background(), stale); err == nil {
		t.Fatal("stale revision mutation was accepted")
	}
}

func TestAnyConnectMutationRollsBackOnPersistenceFailure(t *testing.T) {
	fixture := setupAnyConnectMutationFixture(t, []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active"}})
	beforePassword, err := os.ReadFile(fixture.prepared.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	oldAtomic := runtimePolicyAtomicWrite
	runtimePolicyAtomicWrite = func(string, []byte) error { return fmt.Errorf("injected policy write failure") }
	t.Cleanup(func() { runtimePolicyAtomicWrite = oldAtomic })

	add := anyConnectMutationRequest("add-31-ac-test", "add", 11, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "bob-v1", Status: "active"})
	if _, err := fixture.server.AddUser(context.Background(), add); err == nil {
		t.Fatal("AddUser unexpectedly succeeded despite a failing policy write")
	}
	afterPassword, err := os.ReadFile(fixture.prepared.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterPassword) != string(beforePassword) {
		t.Fatalf("rollback did not restore the credential file: before=%q after=%q", beforePassword, afterPassword)
	}
	if _, err := os.Stat(filepath.Join(fixture.prepared.Files.UserConfigDir, "bob")); !os.IsNotExist(err) {
		t.Fatalf("rollback left a new user config: %v", err)
	}
	current, err := fixture.server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 10 {
		t.Fatalf("failed mutation advanced policy revision to %d", current.Revision)
	}
}

func TestAnyConnectMutationLockWaitHonorsCancellation(t *testing.T) {
	s := &Server{}
	unlock, err := s.lockAnyConnectMutation(context.Background(), "same-operation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.lockAnyConnectMutation(ctx, "same-operation"); err == nil {
		t.Fatal("waiting operation lock ignored cancellation")
	}
	unlock()
	unlockAgain, err := s.lockAnyConnectMutation(context.Background(), "same-operation")
	if err != nil {
		t.Fatalf("operation lock leaked after canceled waiter: %v", err)
	}
	unlockAgain()
}

func TestAnyConnectRemoveUserIdentityRoundTrip(t *testing.T) {
	email := fmt.Sprintf("%s42:17:%s", anyConnectRemoveUserPrefix, "Ym9i")
	userID, revision, username, ok := anyConnectRemoveUserIdentity(email)
	if !ok || userID != 42 || revision != 17 || username != "bob" {
		t.Fatalf("decoded id=%d revision=%d username=%q ok=%v", userID, revision, username, ok)
	}
	if _, _, _, ok := anyConnectRemoveUserIdentity("anyconnect-user:0:17"); ok {
		t.Fatal("zero user id was accepted")
	}
	if _, _, _, ok := anyConnectRemoveUserIdentity("anyconnect-user:42:0"); ok {
		t.Fatal("zero revision was accepted")
	}
}

// TestAnyConnectAnyHelper emulates the subset of ocpasswd used by both the
// full prepare path ("-c FILE -g antimage USERNAME") and the targeted mutation
// path ("-c FILE USERNAME" / "-c FILE -d USERNAME"), preserving unrelated
// entries in every case.
func TestAnyConnectAnyHelper(t *testing.T) {
	if os.Getenv("GO_WANT_ANYCONNECT_ANY_HELPER") != "1" {
		return
	}
	args := os.Args
	separator := 0
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	args = args[separator+1:]
	if len(args) < 3 || args[0] != "-c" {
		os.Exit(2)
	}
	path := args[1]
	remove := false
	username := ""
	switch {
	case len(args) == 3: // -c FILE USERNAME (mutation add/update)
		username = args[2]
	case len(args) == 4 && args[2] == "-d": // -c FILE -d USERNAME (mutation remove)
		remove = true
		username = args[3]
	case len(args) == 5 && args[2] == "-g" && args[4] != "": // -c FILE -g GROUP USERNAME (prepare)
		username = args[4]
	default:
		os.Exit(2)
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		os.Exit(3)
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, username+":") {
			continue
		}
		kept = append(kept, line)
	}
	if !remove {
		// The mutation path writes the password twice on stdin (password +
		// confirmation) exactly like the real ocpasswd prompt.
		_, _ = io.ReadAll(os.Stdin)
		kept = append(kept, username+":antimage:$5$rounds=5000$antimage")
	}
	body := ""
	if len(kept) > 0 {
		body = strings.Join(kept, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

// TestAnyConnectQuickExitHelper exits immediately, standing in for a
// successful "occtl disconnect user" invocation.
func TestAnyConnectQuickExitHelper(t *testing.T) {
	if os.Getenv("GO_WANT_ANYCONNECT_QUICK_EXIT") != "1" {
		return
	}
	os.Exit(0)
}
