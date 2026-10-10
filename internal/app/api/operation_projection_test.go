package api

import (
	"encoding/json"
	"fmt"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"net/http/httptest"
	"strings"
	"testing"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
)

func TestPublicMaintenanceFailureResponsesExcludeRawOutput(t *testing.T) {
	for _, write := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			writeUpdateControllerError(w, fmt.Errorf("Authorization: Bearer response-private-secret https://private.test?token=response-private-secret"))
		},
		func(w *httptest.ResponseRecorder) {
			writeMaintenanceError(w, fmt.Errorf("password=response-private-secret"))
		},
	} {
		recorder := httptest.NewRecorder()
		recorder.Header().Set("X-Request-ID", "request-original")
		write(recorder)
		if strings.Contains(recorder.Body.String(), "response-private-secret") || !strings.Contains(recorder.Body.String(), "request-original") || recorder.Code < 400 {
			t.Fatalf("unsafe failure response: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	writeUpdateControllerError(recorder, fmt.Errorf("%w: %w", nodecontroller.ErrNodeOperationConflict, operationapp.Conflict{BlockingID: "blocking-original"}))
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "blocking-original") {
		t.Fatalf("conflict identity lost: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicRolloutAndNodeHistoryPreserveStateWithoutRecoverySecrets(t *testing.T) {
	target := systemapp.ResolvedInstall{BuildCatalogEntry: systemapp.BuildCatalogEntry{Version: "v2.0.0", Commit: "abcdef1234567", DownloadURL: "https://private.test?token=rollout-private-secret", SHA256: strings.Repeat("a", 64)}}
	child := nodecontroller.NodeUpdateOperation{ID: "child-original", NodeID: 7, Phase: "manual_recovery_required", RunningVersion: "v1.0.0", DesiredVersion: "v2.0.0", ResolvedTarget: &target, Error: "rollout-private-secret", RollbackError: "rollout-private-secret", RecoveryError: "rollout-private-secret", Reason: "rollout-private-secret"}
	original := nodecontroller.RolloutView{Operation: operationapp.Operation{ID: "rollout-original", RequestID: "request-original", State: "waiting", Phase: "canary_verifying", Metadata: map[string]any{"rollout": map[string]any{"download_url": target.DownloadURL}}}, Rollout: nodecontroller.Rollout{ID: "rollout-original", ResolvedTarget: target}, Children: []nodecontroller.NodeUpdateOperation{child}, Summary: map[string]int{"running": 1}}
	public := publicRolloutView(original)
	for _, value := range []any{public, publicNodeUpdate(child), flattenRuntimeResult(nodecontroller.RuntimeResult{NodeID: 7, UpdateOperation: &child})} {
		data, err := json.Marshal(value)
		if err != nil || strings.Contains(string(data), "rollout-private-secret") {
			t.Fatalf("unsafe Node projection: %s %v", data, err)
		}
	}
	if public.Operation.RequestID != original.Operation.RequestID || public.Operation.State != "waiting" || public.Children[0].Phase != child.Phase || public.Children[0].RunningVersion != child.RunningVersion || public.Rollout.ResolvedTarget.Commit != target.Commit {
		t.Fatal("projection fabricated or lost lifecycle evidence")
	}
	if original.Rollout.ResolvedTarget.DownloadURL == "" || original.Children[0].Error == "" || child.ResolvedTarget.DownloadURL == "" {
		t.Fatal("projection changed server recovery state")
	}
}

func TestPublicMaintenanceSnapshotRetainsStateWithoutCommandSecrets(t *testing.T) {
	target := &systemapp.ResolvedInstall{BuildCatalogEntry: systemapp.BuildCatalogEntry{Version: "v2.0.0", DownloadURL: "https://private.test?token=snapshot-private-secret", SHA256: strings.Repeat("a", 64)}}
	original := systemapp.MaintenanceOperationSnapshot{ID: "operation-original", RequestID: "request-original", Phase: "outcome_unknown", Running: true, DesiredVersion: "v2.0.0", RunningVersion: "v1.0.0", ResolvedTarget: target, Message: "snapshot-private-secret", Error: "snapshot-private-secret", RollbackError: "snapshot-private-secret", Reason: "snapshot-private-secret", Logs: []string{"Authorization: Bearer snapshot-private-secret", "Download 37% snapshot-private-secret", "999% snapshot-private-secret"}}
	public := publicMaintenanceSnapshot(original)
	data, err := json.Marshal(public)
	if err != nil || strings.Contains(string(data), "snapshot-private-secret") {
		t.Fatalf("unsafe maintenance snapshot: %s %v", data, err)
	}
	if public.ID != original.ID || public.RequestID != original.RequestID || public.Phase != original.Phase || public.DesiredVersion != original.DesiredVersion || public.RunningVersion != original.RunningVersion || !public.Running {
		t.Fatal("projection lost real operation state")
	}
	if len(public.Logs) != 1 || public.Logs[0] != "Progress: 37%" {
		t.Fatalf("unsafe progress extraction: %v", public.Logs)
	}
	if original.ResolvedTarget.DownloadURL == "" || len(original.Logs) != 3 || original.Error == "" {
		t.Fatal("public projection modified recovery payload")
	}
}

func TestPublicOperationExcludesRecoverySecrets(t *testing.T) {
	original := operationapp.Operation{ID: "op-123", RequestID: "req-123", Error: "password=raw-secret", Metadata: map[string]any{
		"command_id": "cmd-123", "resource_generation": 42,
		"token": "raw-secret", "logs": []string{"raw-secret"},
		"snapshot":          map[string]any{"running_version": "v1.2.3", "resolved_target": map[string]any{"artifact_sha256": "abcdef", "download_url": "https://host/private?token=raw-secret"}, "error": "raw-secret"},
		"requested_version": "v1.2.3\nBearer raw-secret",
	}}
	public := publicOperation(original)
	data, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{"raw-secret", "download_url", "Bearer", "password", "logs"} {
		if strings.Contains(string(data), excluded) {
			t.Fatalf("leaked %q: %s", excluded, data)
		}
	}
	if public.RequestID != original.RequestID || public.Metadata["command_id"] != "cmd-123" || public.Metadata["resource_generation"] != 42 {
		t.Fatalf("lost correlation evidence: %+v", public)
	}
	if original.Error != "password=raw-secret" || original.Metadata["token"] != "raw-secret" {
		t.Fatal("projection mutated persisted recovery state")
	}
}
