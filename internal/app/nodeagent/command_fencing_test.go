//go:build linux

package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	systemapp "github.com/antimage/antimage/internal/app/system"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

// The RPC handler and installed Python journal are real. Only systemd enqueue
// is substituted; full binary replacement/service lifecycle remains separate.
func TestServiceUpdateRPCRejectsPersistedStaleGeneration(t *testing.T) {
	previousEUID, previousCommand := maintenanceEUID, maintenanceCommandContext
	maintenanceEUID = func() int { return 0 }
	t.Cleanup(func() { maintenanceEUID, maintenanceCommandContext = previousEUID, previousCommand })
	app := t.TempDir()
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "node-1")
	t.Setenv("GO_WANT_MAINTENANCE_HELPER_PROCESS", "1")
	helper, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "antimage", "node-command-fence.sh"))
	if err != nil {
		t.Fatal(err)
	}
	enqueued := 0
	maintenanceCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "/usr/local/bin/node-1" && len(args) > 0 && args[0] == "fence-accept" {
			values := map[string]string{}
			for index := 1; index+1 < len(args); index += 2 {
				values[args[index]] = args[index+1]
			}
			return exec.CommandContext(ctx, "bash", "-c", `source "$1"; APP_DIR="$2"; FENCE_OPERATION_ID="$3"; LEASE_GENERATION="$4"; COMMAND_ID="$5"; RESOURCE_GENERATION="$6"; RESOURCE_ID="$7"; node_command_fence accept`, "test", helper, app, values["--fence-operation-id"], values["--lease-generation"], values["--command-id"], values["--resource-generation"], values["--resource-id"])
		}
		if name == "systemd-run" {
			enqueued++
		}
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestMaintenanceHelperProcess")
	}
	target := systemapp.ResolvedInstall{BuildCatalogEntry: systemapp.BuildCatalogEntry{Version: "dev-abcdef0", Channel: "dev", Commit: "abcdef0" + strings.Repeat("0", 33), WorkflowRunID: "123", OS: "linux", Architecture: runtime.GOARCH, Size: 42, SHA256: strings.Repeat("a", 64), ArtifactName: "node", DownloadURL: "https://example.test/node"}}
	payload, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	req := &nodev1.ServiceUpdateRequest{OperationId: "operation", FenceOperationId: "operation", LeaseGeneration: 11, ResourceGeneration: 11, ResourceId: "7", CommandId: "command-new", Channel: "dev", Version: target.Version, ResolvedBuildJson: string(payload)}
	wire, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &nodev1.ServiceUpdateRequest{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	server := New(Config{InstallMode: "binary", Name: "node-1"})
	response, err := server.UpdateService(context.Background(), decoded)
	if err != nil || response == nil || !response.Accepted || response.CommandId != req.CommandId || response.LeaseGeneration != 11 || enqueued != 1 {
		t.Fatalf("fenced request/echo failed: response=%v error=%v queued=%d", response, err, enqueued)
	}
	decoded.LeaseGeneration = 10
	decoded.ResourceGeneration = 10
	decoded.CommandId = "command-old"
	for attempt := 0; attempt < 2; attempt++ {
		server = New(Config{InstallMode: "binary", Name: "node-1"})
		if _, err := server.UpdateService(context.Background(), decoded); err == nil {
			t.Fatal("stale RPC accepted after node server reconstruction")
		}
	}
	if enqueued != 1 {
		t.Fatalf("stale RPC enqueued a second destructive job: %d", enqueued)
	}
	decoded.OperationId = "rollback-operation-b"
	decoded.FenceOperationId = decoded.OperationId
	decoded.CommandId = "command-b"
	decoded.LeaseGeneration = 1
	decoded.ResourceGeneration = 12
	if _, err := server.UpdateService(context.Background(), decoded); err != nil {
		t.Fatal(err)
	}
	decoded.OperationId = "operation"
	decoded.FenceOperationId = decoded.OperationId
	decoded.CommandId = "delayed-operation-a"
	decoded.LeaseGeneration = 99
	decoded.ResourceGeneration = 11
	if _, err := server.UpdateService(context.Background(), decoded); err == nil {
		t.Fatal("older resource generation from another operation accepted")
	}
	if enqueued != 2 {
		t.Fatalf("cross-operation stale request was enqueued: %d", enqueued)
	}
}
