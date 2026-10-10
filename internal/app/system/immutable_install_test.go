package system

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPanelResolvedArtifactReachesSchedulerAndSurvivesReload(t *testing.T) {
	db := newPanelUpdateDB(t)
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(&mutableMaintenanceRuntime{info: RuntimeInfo{Mode: "binary", RunningVersion: "v1.2.2"}}, &captureUpdateChecker{}, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op, err := service.Update(context.Background(), MaintenanceUpdateRequest{Channel: "stable", Version: "v1.2.3", Policy: "pinned"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduler.calls) != 1 || len(scheduler.calls[0]) != 17 || scheduler.calls[0][2] != "v1.2.3" || scheduler.calls[0][3] != "--resolved-build" {
		t.Fatalf("installer lost exact artifact: %+v", scheduler.calls)
	}
	if scheduler.calls[0][5] != "--operation-id" || scheduler.calls[0][6] != op.ID {
		t.Fatal("backup identity is not tied to persisted operation")
	}
	var sent ResolvedInstall
	if err := json.Unmarshal([]byte(scheduler.calls[0][4]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Version != "v1.2.3" || sent.SHA256 != strings.Repeat("a", 64) || sent.Size != 42 || sent.RequestedPolicy != "pinned" {
		t.Fatalf("installer identity=%+v", sent)
	}
	service.ops.AppendOutput(op.ID, "Resolved AntiMage version: v9.9.9")
	reloaded := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	got := reloaded.Latest()
	if got.ResolvedTarget == nil || *got.ResolvedTarget != sent || got.ResolvedVersion != "v1.2.3" {
		t.Fatalf("persisted target changed: %+v", got)
	}
	got.ResolvedTarget.Version = "v9.9.9"
	if reloaded.Latest().ResolvedTarget.Version != "v1.2.3" {
		t.Fatal("snapshot caller mutated persisted target")
	}
}
