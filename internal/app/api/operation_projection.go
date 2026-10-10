package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"net/http"
	"regexp"
	"strconv"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
)

var operationEvidenceIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_.:/ -]{1,160}$`)
var publicProgressPercent = regexp.MustCompile(`(?:^|\s)([0-9]{1,3})%`)

func writeUpdateControllerError(w http.ResponseWriter, err error) {
	status, detail := http.StatusBadGateway, "Node maintenance could not be verified. Inspect diagnostics using the request ID."
	if errors.Is(err, nodecontroller.ErrNodeOperationConflict) {
		status, detail = http.StatusConflict, "A conflicting maintenance operation owns this node. Inspect active operation history."
	} else if errors.Is(err, sql.ErrNoRows) {
		status, detail = http.StatusNotFound, "Requested node maintenance resource was not found."
	}
	payload := map[string]any{"detail": detail}
	if id := w.Header().Get("X-Request-ID"); id != "" {
		payload["request_id"] = id
	}
	var conflict operationapp.Conflict
	if errors.As(err, &conflict) && operationEvidenceIdentifier.MatchString(conflict.BlockingID) {
		payload["blocking_operation_id"] = conflict.BlockingID
	}
	writeJSON(w, status, payload)
}

func publicNodeUpdate(update nodecontroller.NodeUpdateOperation) nodecontroller.NodeUpdateOperation {
	if update.ResolvedTarget != nil {
		target := *update.ResolvedTarget
		target.DownloadURL = ""
		update.ResolvedTarget = &target
	}
	update.Reason = ""
	if update.Error != "" {
		update.Error = "Node update could not be verified. Inspect diagnostics using the operation ID."
	}
	if update.RollbackError != "" {
		update.RollbackError = "Rollback could not be verified. Manual recovery is required."
	}
	if update.RecoveryError != "" {
		update.RecoveryError = "Recovery could not prove the outcome. Inspect retained evidence before releasing ownership."
	}
	return update
}

func publicRolloutView(view nodecontroller.RolloutView) nodecontroller.RolloutView {
	view.Operation = publicOperation(view.Operation)
	view.Rollout.ResolvedTarget.DownloadURL = ""
	children := make([]nodecontroller.NodeUpdateOperation, len(view.Children))
	for index, child := range view.Children {
		children[index] = publicNodeUpdate(child)
	}
	view.Children = children
	return view
}

// Keep durable recovery payloads and raw subprocess output on the server.
// Browser status and websocket consumers receive the real state/version tuple
// and numeric progress without signed URLs, credentials or command output.
func publicMaintenanceSnapshot(snapshot systemapp.MaintenanceOperationSnapshot) systemapp.MaintenanceOperationSnapshot {
	if snapshot.ResolvedTarget != nil {
		target := *snapshot.ResolvedTarget
		target.DownloadURL = ""
		snapshot.ResolvedTarget = &target
	}
	if snapshot.Error != "" {
		snapshot.Error = "Operation could not be verified. Inspect diagnostics using its operation and request IDs."
	}
	if snapshot.RollbackError != "" {
		snapshot.RollbackError = "Rollback could not be verified. Inspect the retained backup and recovery diagnostics."
	}
	snapshot.Reason = ""
	if snapshot.Message != "" {
		snapshot.Message = "Maintenance operation state changed."
		if operationEvidenceIdentifier.MatchString(snapshot.Phase) {
			snapshot.Message = "Operation phase: " + snapshot.Phase
		}
	}
	logs := make([]string, 0)
	for _, line := range snapshot.Logs {
		match := publicProgressPercent.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		percent, err := strconv.Atoi(match[1])
		if err == nil && percent <= 100 {
			logs = append(logs, fmt.Sprintf("Progress: %d%%", percent))
		}
		if len(logs) == 80 {
			break
		}
	}
	snapshot.Logs = logs
	return snapshot
}

// The database retains recovery payloads, while the history API exposes only
// bounded identity and evidence fields. Arbitrary payloads and command output
// are not suitable for a browser-facing technical inspector.
func publicOperation(op operationapp.Operation) operationapp.Operation {
	if op.Error != "" {
		op.Error = "Operation could not be verified. Use the operation and request IDs to inspect server diagnostics."
	}
	op.Metadata = publicOperationMetadata(op.Metadata, 0)
	return op
}

func publicOperationMetadata(input map[string]any, depth int) map[string]any {
	output := make(map[string]any)
	if depth > 3 {
		return output
	}
	for _, key := range []string{
		"origin", "command_id", "rollout_id", "node_capability_level", "executor_id", "lease_generation", "resource_generation",
		"requested_channel", "requested_policy", "requested_version", "resolved_version", "resolved_commit", "artifact_name", "artifact_sha256", "artifact_size",
		"previous_version", "running_version", "desired_version", "installed_version", "backup_identity", "error_code", "phase", "action", "version",
		"attempt_count", "deadline", "transaction_recovery_dispatched", "transaction_recovery_action", "finalization_dispatched",
	} {
		switch value := input[key].(type) {
		case string:
			if operationEvidenceIdentifier.MatchString(value) {
				output[key] = value
			}
		case bool, int, int64, float64, json.Number:
			output[key] = value
		}
	}
	for _, key := range []string{"snapshot", "update", "resolved_target"} {
		if child, ok := input[key].(map[string]any); ok {
			output[key] = publicOperationMetadata(child, depth+1)
		}
	}
	return output
}
