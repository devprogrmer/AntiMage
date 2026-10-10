//go:build linux

package nodeagent

import (
	"context"
	managedprocess "github.com/antimage/antimage/internal/platform/process"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"os"
	"os/exec"
	"testing"
)

func TestStopReceiptSurvivesLostACKWithoutProcessReplay(t *testing.T) {
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	s := New(Config{DataDir: t.TempDir(), InstallMode: "script"})
	cmd := managedprocess.CommandContext(context.Background(), "sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	s.lastRuntime = cmd
	s.runtimeExited = exited
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		if s.lastRuntime == cmd {
			s.lastRuntime = nil
		}
		s.mu.Unlock()
		close(exited)
	}()
	t.Cleanup(func() { _ = s.stopRuntime() })
	fence := &nodev1.DestructiveFence{OperationId: "stop-op", CommandId: "stop-command", ResourceId: "1", ResourceGeneration: 41, LeaseGeneration: 1}
	// Discard the successful response to model an ACK lost after actual exit.
	if _, err := s.StopRuntime(context.Background(), &nodev1.StopRuntimeRequest{OperationId: fence.OperationId, Fence: fence}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(os.Signal(nil)); err == nil {
		t.Fatal("stopped process still accepts signals")
	}
	req := &nodev1.HealthRequest{OperationId: fence.OperationId, CommandId: fence.CommandId}
	response, err := s.Health(context.Background(), req)
	if err != nil || !response.GetRuntime().GetRuntimeStopVerified() || response.GetRuntime().GetEvidenceCommandState() != "completed" {
		t.Fatalf("stop evidence: %v %v", response, err)
	}
	if _, err := s.StopRuntime(context.Background(), &nodev1.StopRuntimeRequest{OperationId: fence.OperationId, Fence: fence}); err == nil {
		t.Fatal("stop replay accepted")
	}
	// Reconstruct the node process: persisted receipt remains, but a new node
	// process cannot claim to have inspected runtimes owned by the old process.
	restarted := New(s.cfg)
	response, err = restarted.Health(context.Background(), req)
	if err != nil || response.GetRuntime().GetRuntimeStopVerified() {
		t.Fatalf("fresh process fabricated stop evidence: %v %v", response, err)
	}
	// A real supervisor-created replacement runtime invalidates stop evidence.
	replacement := exec.Command("sleep", "120")
	if err := replacement.Start(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.lastRuntime = replacement
	s.mu.Unlock()
	response, err = s.Health(context.Background(), req)
	_ = replacement.Process.Kill()
	_ = replacement.Wait()
	s.mu.Lock()
	s.lastRuntime = nil
	s.mu.Unlock()
	if err != nil || response.GetRuntime().GetRuntimeStopVerified() {
		t.Fatalf("running replacement accepted: %v %v", response, err)
	}
	newer := &nodev1.DestructiveFence{OperationId: "new-owner", CommandId: "new-command", ResourceId: "1", ResourceGeneration: 42, LeaseGeneration: 1}
	if err := acceptNativeDestructiveFence(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	response, err = s.Health(context.Background(), req)
	if err != nil || response.GetRuntime().GetEvidenceCommandState() != "superseded" || response.GetRuntime().GetRuntimeStopVerified() {
		t.Fatalf("superseded receipt accepted: %v %v", response, err)
	}
}
