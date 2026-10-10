//go:build linux

package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	systemapp "github.com/antimage/antimage/internal/app/system"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestVerifiedNodeBackupPreflightAndRollbackCommand(t *testing.T) {
	previousEUID, previousCommand := maintenanceEUID, maintenanceCommandContext
	maintenanceEUID = func() int { return 0 }
	t.Cleanup(func() { maintenanceEUID, maintenanceCommandContext = previousEUID, previousCommand })
	app := t.TempDir()
	folder := filepath.Join(app, ".update-backups", "update-source")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	if err := os.WriteFile(filepath.Join(folder, "0"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	backup := systemapp.BinaryBackup{Identity: "update-source", TargetType: "node", TargetID: "node-1", Version: "v1.2.3", Commit: strings.Repeat("a", 40), OS: "linux", Architecture: runtime.GOARCH, SourceOperationID: "update-source", Files: []systemapp.BackupFile{{Reference: "0", Destination: filepath.Join(app, "antimage-node"), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(binary)), Mode: 0750}}}
	payload, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "manifest.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_NODE_APP_DIR", app)
	t.Setenv("ANTIMAGE_NODE_APP_NAME", "node-1")
	t.Setenv("GO_WANT_MAINTENANCE_HELPER_PROCESS", "1")
	var commands [][]string
	maintenanceCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		commands = append(commands, append([]string{name}, args...))
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestMaintenanceHelperProcess")
	}
	server := New(Config{InstallMode: "binary", Name: "node-1"})
	request := &nodev1.ServiceUpdateRequest{Action: "validate_backup", BackupIdentity: backup.Identity, Version: backup.Version}
	response, err := server.UpdateService(context.Background(), request)
	if err != nil || response == nil || !response.Accepted || response.BackupJson == "" || len(commands) != 0 {
		t.Fatalf("preflight: response=%v err=%v commands=%v", response, err, commands)
	}
	request.Action = "rollback"
	request.OperationId = "rollback-manual"
	request.FenceOperationId = "rollback-manual"
	request.LeaseGeneration = 11
	request.ResourceGeneration = 11
	request.ResourceId = "7"
	request.CommandId = "command-rollback"
	response, err = server.UpdateService(context.Background(), request)
	if err != nil || response == nil || !response.Accepted || response.OperationId != request.OperationId || response.CommandId != request.CommandId || response.LeaseGeneration != 11 || len(commands) != 2 {
		t.Fatalf("rollback scheduling: response=%v err=%v commands=%v", response, err, commands)
	}
	command := strings.Join(commands[1], " ")
	if !strings.Contains(command, "rollback --backup-id update-source --version v1.2.3 --operation-id rollback-manual") {
		t.Fatalf("incorrect production restore command: %s", command)
	}
	if err := os.WriteFile(filepath.Join(folder, "0"), []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := server.UpdateService(context.Background(), request); err == nil {
		t.Fatal("corrupt backup accepted")
	}
	if len(commands) != 2 {
		t.Fatal("corrupt backup scheduled restore")
	}
}
