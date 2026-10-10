//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func acceptNativeTestCommand(t *testing.T, fence *nodev1.DestructiveFence, app string) error {
	t.Helper()
	helper, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "antimage", "node-command-fence.sh"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "bash", "-c", `source "$1"; APP_DIR="$2"; FENCE_OPERATION_ID="$3"; LEASE_GENERATION="$4"; COMMAND_ID="$5"; RESOURCE_GENERATION="$6"; RESOURCE_ID="$7"; node_command_fence accept`, "test", helper, app, fence.OperationId, fmt.Sprint(fence.LeaseGeneration), fence.CommandId, fmt.Sprint(fence.ResourceGeneration), fence.ResourceId).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func TestNativeLegacyBoundarySharesInstalledResourceGeneration(t *testing.T) {
	for _, action := range []string{"core_update", "core_restart", "sync_config", "geo_activation", "node_restart"} {
		t.Run(action, func(t *testing.T) {
			app := t.TempDir()
			t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
			old := &nodev1.DestructiveFence{OperationId: action, CommandId: "command-old", ResourceId: "1", ResourceGeneration: 100, LeaseGeneration: 1}
			if err := acceptNativeTestCommand(t, old, app); err != nil {
				t.Fatal(err)
			}
			newer := &nodev1.DestructiveFence{OperationId: "node-rollback", CommandId: "command-new", ResourceId: "1", ResourceGeneration: 101, LeaseGeneration: 1}
			if err := acceptNativeTestCommand(t, newer, app); err != nil {
				t.Fatal(err)
			}
			count := 0
			apply := func(context.Context) (*nodev1.RuntimeActionResponse, error) {
				count++
				return &nodev1.RuntimeActionResponse{Accepted: true}, nil
			}
			if _, err := runNativeDestructiveBoundary(context.Background(), old, apply); err == nil {
				t.Fatal("stale legacy boundary executed")
			}
			if _, err := runNativeDestructiveBoundary(context.Background(), newer, apply); err != nil {
				t.Fatal(err)
			}
			if _, err := runNativeDestructiveBoundary(context.Background(), newer, apply); err == nil {
				t.Fatal("completed command replayed")
			}
			if count != 1 {
				t.Fatalf("destructive execution count=%d", count)
			}
		})
	}
}

func TestNativeInterruptedCommandRequiresReconciliation(t *testing.T) {
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	fence := &nodev1.DestructiveFence{OperationId: "sync-config", CommandId: "command-config", ResourceId: "1", ResourceGeneration: 120, LeaseGeneration: 1}
	if err := acceptNativeTestCommand(t, fence, app); err != nil {
		t.Fatal(err)
	}
	count := 0
	apply := func(context.Context) (*nodev1.RuntimeActionResponse, error) {
		count++
		return nil, context.DeadlineExceeded
	}
	if _, err := runNativeDestructiveBoundary(context.Background(), fence, apply); err == nil {
		t.Fatal("lost outcome accepted")
	}
	if err := acceptNativeTestCommand(t, fence, app); err == nil {
		t.Fatal("unknown outcome accepted for replay")
	}
	fence.LeaseGeneration++
	if err := acceptNativeTestCommand(t, fence, app); err == nil {
		t.Fatal("takeover replayed unknown command")
	}
	if _, err := runNativeDestructiveBoundary(context.Background(), fence, apply); err == nil {
		t.Fatal("interrupted destructive action replayed")
	}
	if count != 1 {
		t.Fatalf("destructive command executed %d times", count)
	}
}

func TestNonBinaryNativeFencingSharesCLIJournal(t *testing.T) {
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	s := New(Config{DataDir: t.TempDir(), InstallMode: "script"})
	if _, err := s.SyncConfig(context.Background(), &nodev1.RuntimeConfigRequest{}); err == nil {
		t.Fatal("non-binary unowned runtime mutation accepted")
	}
	fence := &nodev1.DestructiveFence{OperationId: "native-config", CommandId: "native-command", ResourceId: "1", ResourceGeneration: 200, LeaseGeneration: 1}
	calls := 0
	apply := func(context.Context) (*nodev1.RuntimeActionResponse, error) {
		calls++
		return &nodev1.RuntimeActionResponse{Accepted: true}, nil
	}
	response, err := s.fencedRuntimeAction(context.Background(), fence.OperationId, fence, apply)
	if err != nil || response.GetResourceGeneration() != 200 || calls != 1 {
		t.Fatalf("non-binary native fencing: %+v %v", response, err)
	}
	if err := acceptNativeTestCommand(t, fence, app); err == nil {
		t.Fatal("CLI replayed native completed command")
	}
	newer := &nodev1.DestructiveFence{OperationId: "cli-rollback", CommandId: "cli-command", ResourceId: "1", ResourceGeneration: 201, LeaseGeneration: 1}
	if err := acceptNativeTestCommand(t, newer, app); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fencedRuntimeAction(context.Background(), fence.OperationId, fence, apply); err == nil {
		t.Fatal("native action bypassed newer CLI resource owner")
	}
	if calls != 1 {
		t.Fatal("stale non-binary action executed")
	}
	for _, capability := range s.runtimeState("").GetCapabilities() {
		if capability == "verified_updates_v1" {
			t.Fatal("non-binary agent claimed binary replacement support")
		}
	}
}
