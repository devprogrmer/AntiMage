package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func xrayValidationHelperCommand(ctx context.Context, args []string, mode string) *exec.Cmd {
	commandArgs := append([]string{"-test.run=^TestXrayConfigValidationHelperProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], commandArgs...)
	cmd.Env = append(os.Environ(), "ANTIMAGE_XRAY_VALIDATION_TEST="+mode)
	return cmd
}

func TestXrayConfigValidationHelperProcess(t *testing.T) {
	mode := os.Getenv("ANTIMAGE_XRAY_VALIDATION_TEST")
	if mode == "" {
		return
	}
	for index, arg := range os.Args {
		if arg != "--" {
			continue
		}
		args := os.Args[index+1:]
		if len(args) != 4 || args[0] != "run" || args[1] != "-test" || args[2] != "-config" {
			t.Fatalf("unexpected validation invocation: %v", args)
		}
		raw, err := os.ReadFile(args[3])
		if err != nil || !json.Valid(raw) {
			t.Fatalf("candidate config was not staged: %v", err)
		}
		if mode == "reject" {
			t.Fatal("injected target-core config rejection")
		}
		return
	}
	t.Fatal("validation arguments missing")
}

func TestXrayConfigValidationFailurePreservesRuntimeAndOfflinePolicy(t *testing.T) {
	s, adds, _, _ := xrayLiveRPCFixture(t)
	old := &nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[]}}]}`, DesiredRevision: 1}
	if err := os.WriteFile(s.lastConfig, []byte(old.ConfigJson), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.persistRuntimePolicy(old); err != nil {
		t.Fatal(err)
	}
	s.appliedRev = 1
	if err := s.ensureXrayUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "must-survive-config-rejection", Samples: []xrayUsageSample{{UserID: 1, InboundTag: "vless", Value: 123}}}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	witness, generation := s.lastRuntime, s.xrayRuntimeGeneration
	previous := xrayCommandContext
	xrayCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return xrayValidationHelperCommand(ctx, args, "reject")
	}
	defer func() { xrayCommandContext = previous }()
	_, err := s.SyncConfig(context.Background(), &nodev1.RuntimeConfigRequest{ConfigJson: `{"inbounds":[{"tag":"API_INBOUND","port":54321,"protocol":"invalid"}]}`, DesiredRevision: 2})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid target config was not rejected: %v", err)
	}
	if s.lastRuntime != witness || s.xrayRuntimeGeneration != generation || s.appliedRev != 1 || s.cfg.XrayAPIPort != 12345 || *adds != 0 {
		t.Fatal("rejected candidate changed active runtime, API port, revision or users")
	}
	raw, err := os.ReadFile(s.lastConfig)
	if err != nil || string(raw) != old.ConfigJson {
		t.Fatal("rejected candidate replaced the applied core config")
	}
	restarted := New(s.cfg)
	policy, err := restarted.loadRuntimePolicy()
	if err != nil || policy == nil || policy.Revision != 1 || policy.ConfigJSON != old.ConfigJson {
		t.Fatal("rejected candidate poisoned offline restart policy")
	}
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil || restarted.xrayUsagePending == nil || restarted.xrayUsagePending.BatchID != "must-survive-config-rejection" || restarted.xrayUsagePending.Samples[0].Value != 123 {
		t.Fatal("rejected candidate lost unacknowledged accounting")
	}
	files, err := filepath.Glob(filepath.Join(s.cfg.DataDir, ".xray-config-check-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("candidate credential file was left behind")
	}
}

func TestXrayConfigValidationCancellationCleansCandidate(t *testing.T) {
	s := New(Config{DataDir: t.TempDir(), XrayPath: os.Args[0]})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.validateXrayRuntimeConfig(ctx, `{}`); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled check was accepted: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(s.cfg.DataDir, ".xray-config-check-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("cancellation left candidate credentials behind")
	}
}

func TestXrayConfigValidationReusesUnchangedRunningConfig(t *testing.T) {
	s, _, _, _ := xrayLiveRPCFixture(t)
	config := `{"inbounds":[],"outbounds":[]}`
	if err := os.WriteFile(s.lastConfig, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	previous := xrayCommandContext
	defer func() { xrayCommandContext = previous }()
	invoked := false
	xrayCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		invoked = true
		return xrayValidationHelperCommand(ctx, args, "reject")
	}
	if err := s.validateXrayRuntimeConfig(context.Background(), config); err != nil || invoked {
		t.Fatalf("unchanged running config spawned another core check: invoked=%v err=%v", invoked, err)
	}
}

func TestXrayConfigCheckOutputIsBounded(t *testing.T) {
	output := &xrayConfigCheckOutput{}
	raw := bytes.Repeat([]byte("x"), 1<<20)
	for i := 0; i < 2; i++ {
		if n, err := output.Write(raw); err != nil || n != len(raw) {
			t.Fatal("diagnostic writer interrupted the subprocess")
		}
	}
	if output.Len() != 4096 {
		t.Fatal("unbounded subprocess diagnostics")
	}
}
