package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestRuntimePolicySnapshotRestartAndIntegrity(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	req := &nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[]}`, OvRuntimeJson: `{"xray_policies":[{"user_id":10,"email":"10.a","inbound_tag":"a","status":"active","used_traffic":300,"data_limit":500,"usage_coefficient":2,"inbound_coefficient":1.5,"device_limit":2,"ip_limit":1,"upload_speed_limit":123,"download_speed_limit":456}]}`, DesiredRevision: 42}
	if err := s.persistRuntimePolicy(req); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{cfg: s.cfg}
	snapshot, err := restarted.loadRuntimePolicy()
	if err != nil || snapshot == nil || snapshot.Revision != 42 || snapshot.NativeJSON != req.OvRuntimeJson {
		t.Fatalf("snapshot=%v err=%v", snapshot, err)
	}
	payload, err := parseNativeRuntimePayload(snapshot.NativeJSON)
	if err != nil || len(payload.XrayPolicies) != 1 {
		t.Fatal("policy did not survive restart")
	}
	p := payload.XrayPolicies[0]
	if p.DeviceLimit != 2 || p.IPLimit != 1 || p.UsageCoefficient != 2 || p.InboundCoefficient != 1.5 || p.UploadSpeedLimit != 123 || p.DownloadSpeedLimit != 456 {
		t.Fatalf("policy fields lost: %+v", p)
	}
	// A valid JSON edit without a matching checksum must fail closed.
	snapshot.NativeJSON = `{}`
	raw, _ := json.Marshal(snapshot)
	if err := os.WriteFile(s.runtimePolicyPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := restarted.restoreRuntimePolicy(context.Background()); err == nil {
		t.Fatal("corrupt policy activated")
	}
	got, _ := os.ReadFile(s.runtimePolicyPath())
	if string(got) != string(raw) {
		t.Fatal("corrupt policy evidence overwritten")
	}
}

func TestRuntimePolicyWriteFailureDoesNotReplaceLastValid(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	first := &nodev1.RuntimeConfigRequest{ConfigJson: `{}`, DesiredRevision: 1}
	if err := s.persistRuntimePolicy(first); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.runtimePolicyPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.runtimePolicyPath(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, DesiredRevision: 2}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if s.runtimePolicy.Revision != 1 {
		t.Fatal("failed persist published newer policy")
	}
	if _, err := os.Stat(filepath.Dir(s.runtimePolicyPath())); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePolicyAtomicWriteHookPreservesPublishedSnapshotOnFailure(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	first := &nodev1.RuntimeConfigRequest{ConfigJson: `{}`, DesiredRevision: 1}
	if err := s.persistRuntimePolicy(first); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.runtimePolicyPath())
	if err != nil {
		t.Fatal(err)
	}
	previous := runtimePolicyAtomicWrite
	runtimePolicyAtomicWrite = func(string, []byte) error { return errors.New("injected atomic policy write failure") }
	t.Cleanup(func() { runtimePolicyAtomicWrite = previous })
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: `{}`, DesiredRevision: 2}); err == nil {
		t.Fatal("injected policy write failure was ignored")
	}
	after, err := os.ReadFile(s.runtimePolicyPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || s.runtimePolicy.Revision != 1 {
		t.Fatalf("failed write changed policy: memory=%+v diskChanged=%v", s.runtimePolicy, string(after) != string(before))
	}
}

func TestCurrentRuntimePolicyReturnsIsolatedSnapshot(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	want := &nodev1.RuntimeConfigRequest{ConfigJson: `{}`, OvRuntimeJson: `{"xray_policies":[{"email":"alice"}]}`, DesiredRevision: 7}
	if err := s.persistRuntimePolicy(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.currentRuntimePolicy()
	if err != nil || got == nil {
		t.Fatalf("current runtime policy: snapshot=%v err=%v", got, err)
	}
	got.NativeJSON = `{}`
	got.Revision = 99
	current, err := s.currentRuntimePolicy()
	if err != nil || current.NativeJSON != want.OvRuntimeJson || current.Revision != 7 {
		t.Fatalf("caller mutation escaped snapshot copy: snapshot=%+v err=%v", current, err)
	}
}
