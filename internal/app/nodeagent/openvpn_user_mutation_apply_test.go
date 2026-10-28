package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestOpenVPNTargetedRPCAddUpdateRemoveKeepsManagedProcessAndReplaysACK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenVPN process lifecycle fixture requires Unix process semantics")
	}
	oldLookPath, oldCommand := openVPNLookPath, openVPNCommandContext
	oldTerminate, oldGrace := openVPNTerminateProcess, openVPNStartupGrace
	oldStatus, oldKill := openVPNMutationManagementStatus, openVPNMutationManagementClientKill
	oldCheckpoint := openVPNMutationCheckpoint
	t.Cleanup(func() {
		openVPNLookPath, openVPNCommandContext = oldLookPath, oldCommand
		openVPNTerminateProcess, openVPNStartupGrace = oldTerminate, oldGrace
		openVPNMutationManagementStatus, openVPNMutationManagementClientKill = oldStatus, oldKill
		openVPNMutationCheckpoint = oldCheckpoint
	})
	openVPNLookPath = func(string) (string, error) { return os.Args[0], nil }
	openVPNStartupGrace = 40 * time.Millisecond
	openVPNTerminateProcess = func(process *os.Process) error { return process.Kill() }
	var starts atomic.Int32
	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, name, "-test.run=TestOpenVPNHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}
	openVPNMutationCheckpoint = func(*Server, context.Context) error { return nil }
	var statusCalls, killCalls atomic.Int32
	openVPNMutationManagementStatus = func(string, string) ([]openVPNStatusClient, error) {
		statusCalls.Add(1)
		return []openVPNStatusClient{{ClientID: "17", Username: "bob"}}, nil
	}
	openVPNMutationManagementClientKill = func(_, _ string, clientID string) error {
		if clientID != "17" {
			t.Fatalf("unexpected OpenVPN client id killed: %s", clientID)
		}
		killCalls.Add(1)
		return nil
	}

	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllOpenVPNRuntimes)
	inbound := openVPNRuntimeInbound{Tag: "ov-test", Port: 1194, Transport: "udp", Settings: map[string]any{"ipv4_pool_cidr": "10.78.0.0/24", "ca": "test", "server_certificate": "test", "server_key": "test", "accounting_enabled": false}, Users: []openVPNRuntimeUser{{UserID: 1, Username: "alice", VPNUsername: "alice", Password: "alice-v1", Status: "active"}}}
	configPath, err := server.prepareOpenVPNInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.startOpenVPNInbound(inbound.Tag, configPath); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	runtimeBefore := server.openVPNRuntimes[inbound.Tag]
	pidBefore := runtimeBefore.cmd.Process.Pid
	server.mu.Unlock()
	native, err := json.Marshal(nativeRuntimePayload{OpenVPNInbounds: []openVPNRuntimeInbound{inbound}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: string(native), DesiredRevision: 10}); err != nil {
		t.Fatal(err)
	}

	add := openVPNMutationRequest("add-11-ov-test", "add", 11, openVPNRuntimeUser{UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-v1", Status: "active"})
	if response, err := server.AddUser(context.Background(), add); err != nil || !response.GetAccepted() {
		t.Fatalf("AddUser response=%v err=%v", response, err)
	}
	update := openVPNMutationRequest("update-12-ov-test", "update", 12, openVPNRuntimeUser{UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-v2", Status: "active"})
	if response, err := server.UpdateUser(context.Background(), update); err != nil || !response.GetAccepted() {
		t.Fatalf("UpdateUser response=%v err=%v", response, err)
	}
	remove := &nodev1.RemoveInboundUserRequest{OperationId: "remove-13-ov-test", InboundTag: inbound.Tag,
		Email: fmt.Sprintf("%s2:13:%s", openVPNRemoveUserPrefix, "Ym9i")}
	if response, err := server.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("RemoveUser response=%v err=%v", response, err)
	}
	remove.Email = fmt.Sprintf("%s2:14:%s", openVPNRemoveUserPrefix, "Ym9i")
	if response, err := server.RemoveUser(context.Background(), remove); err != nil || !response.GetAccepted() {
		t.Fatalf("lost-ACK retry response=%v err=%v", response, err)
	}
	server.mu.Lock()
	runtimeAfter := server.openVPNRuntimes[inbound.Tag]
	server.mu.Unlock()
	if starts.Load() != 1 || runtimeAfter != runtimeBefore || runtimeAfter.cmd.Process.Pid != pidBefore {
		t.Fatalf("targeted mutations replaced OpenVPN process: starts=%d before=%d after=%d", starts.Load(), pidBefore, runtimeAfter.cmd.Process.Pid)
	}
	if statusCalls.Load() != 1 || killCalls.Load() != 1 {
		t.Fatalf("remove ACK replay repeated runtime revoke: status=%d kills=%d", statusCalls.Load(), killCalls.Load())
	}
	current, err := server.currentRuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseNativeRuntimePayload(current.NativeJSON)
	if err != nil || current.Revision != 14 || len(got.OpenVPNInbounds) != 1 || len(got.OpenVPNInbounds[0].Users) != 1 || got.OpenVPNInbounds[0].Users[0].UserID != 1 {
		t.Fatalf("targeted policy mutation changed unrelated user or retained removed user: %+v err=%v", got.OpenVPNInbounds, err)
	}
	root := filepath.Dir(configPath)
	credentials, err := os.ReadFile(filepath.Join(root, "credentials.sha256"))
	if err != nil || strings.Contains(string(credentials), "bob") {
		t.Fatalf("removed user credential file still includes bob: err=%v", err)
	}
}

func openVPNMutationRequest(operationID, operation string, revision uint64, desired openVPNRuntimeUser) *nodev1.InboundUserRequest {
	raw, _ := json.Marshal(desired)
	return &nodev1.InboundUserRequest{OperationId: operationID, InboundTag: "ov-test", User: &nodev1.InboundUser{Protocol: "openvpn", Fields: map[string]string{
		openVPNMutationUserIDField: fmt.Sprint(desired.UserID), openVPNMutationOpField: operation,
		openVPNMutationRevisionField: fmt.Sprint(revision), openVPNMutationUserField: string(raw),
	}}}
}

func TestDecodeOpenVPNMutationRequiresOperationAndRevision(t *testing.T) {
	desired := openVPNRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "secret", Status: "active"}
	raw, _ := json.Marshal(desired)
	user := &nodev1.InboundUser{Protocol: "openvpn", Fields: map[string]string{
		openVPNMutationUserIDField: "7", openVPNMutationOpField: "add",
		openVPNMutationRevisionField: "9", openVPNMutationUserField: string(raw),
	}}
	id, revision, operation, username, got, err := decodeOpenVPNMutationUser(user, "add")
	if err != nil || id != 7 || revision != 9 || operation != "add" || username != "alice" || got == nil || got.Password != "secret" {
		t.Fatalf("decoded mutation id=%d revision=%d operation=%q username=%q user=%+v err=%v", id, revision, operation, username, got, err)
	}
	user.Fields[openVPNMutationRevisionField] = ""
	if _, _, _, _, _, err := decodeOpenVPNMutationUser(user, "add"); err == nil {
		t.Fatal("mutation without a revision was accepted")
	}
	user.Fields[openVPNMutationRevisionField] = "9"
	user.Fields[openVPNMutationUserIDField] = "7junk"
	if _, _, _, _, _, err := decodeOpenVPNMutationUser(user, "add"); err == nil {
		t.Fatal("mutation with a malformed user id was accepted")
	}
}

func TestOpenVPNMutationReceiptIsDurableAndRejectsOperationIDReuse(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{DataDir: dir}}
	digest, err := openVPNMutationDigest("ov-main", 7, "add", "alice", &openVPNRuntimeUser{UserID: 7, VPNUsername: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recordOpenVPNMutationCompleted("add-123-ov-main", digest); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{cfg: Config{DataDir: dir}}
	completed, err := restarted.openVPNMutationAlreadyCompleted("add-123-ov-main", digest)
	if err != nil || !completed {
		t.Fatalf("durable receipt completed=%v err=%v", completed, err)
	}
	otherDigest, _ := openVPNMutationDigest("ov-main", 8, "add", "bob", &openVPNRuntimeUser{UserID: 8, VPNUsername: "bob"})
	if _, err := restarted.openVPNMutationAlreadyCompleted("add-123-ov-main", otherDigest); err == nil {
		t.Fatal("conflicting reuse of operation id was accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(restarted.openVPNMutationResultPath("add-123-ov-main")))
	if err != nil || len(entries) != 1 {
		t.Fatalf("receipt directory entries=%d err=%v", len(entries), err)
	}
}

func TestOpenVPNMutationLockWaitHonorsCancellation(t *testing.T) {
	s := &Server{}
	unlock, err := s.lockOpenVPNMutation(context.Background(), "same-operation")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.lockOpenVPNMutation(ctx, "same-operation"); err == nil {
		t.Fatal("waiting operation lock ignored cancellation")
	}
	unlock()
	unlockAgain, err := s.lockOpenVPNMutation(context.Background(), "same-operation")
	if err != nil {
		t.Fatalf("operation lock leaked after canceled waiter: %v", err)
	}
	unlockAgain()
}
