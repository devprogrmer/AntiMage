package nodecontroller

import (
	"context"
	"encoding/json"
	"errors"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"time"
)

// Startup consumes actual installer/restore files before deciding which single
// missing step can run. The original reconnect/health deadlines never reset.
func (c Controller) reconcileNodeTransaction(ctx context.Context, op NodeUpdateOperation, restore bool) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, _, err := c.dial(probeCtx, op.NodeID)
	if err != nil {
		return true
	} // The existing bounded reconnect wait handles offline.
	backup := op.BackupIdentity
	if restore && backup == "" {
		backup = op.ID
	}
	response, err := client.Control().Health(probeCtx, &nodev1.HealthRequest{OperationId: op.ID, TransactionBackupId: backup, InspectRestore: restore})
	if err != nil {
		return true
	}
	state := response.GetRuntime()
	var evidence systemapp.BinaryTransactionEvidence
	if json.Unmarshal([]byte(state.GetMaintenanceTransactionJson()), &evidence) != nil || evidence.OperationID != op.ID || !evidence.BackupValid {
		_ = c.recoveryFailure(ctx, op, "transaction/backup evidence unavailable; no installer replayed")
		return false
	}
	target := op.DesiredVersion
	if restore && op.Action != "rollback" {
		target = op.PreviousVersion
	}
	if evidence.TargetVersion != target || (!restore && (op.ResolvedTarget == nil || evidence.ArtifactSHA256 != op.ResolvedTarget.SHA256 || !evidence.ArtifactValid)) {
		_ = c.recoveryFailure(ctx, op, "transaction artifact differs from persisted exact target")
		return false
	}
	if evidence.FilesMatch && state.GetNodeVersion() == target && state.GetProcessStartedAtUnixNano() > op.RestartRequestedAt.UnixNano() && state.GetStarted() {
		return true
	}
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil {
		return false
	}
	if stored.Metadata["transaction_recovery_dispatched"] == true {
		return true
	}
	action := ""
	switch evidence.NextAction {
	case "resume_install":
		if !restore {
			action = "resume_install"
		}
	case "resume_restore":
		if restore {
			action = "resume_restore"
		}
	case "restart_only":
		action = "resume_restart"
	}
	if action == "" || time.Now().After(op.ReconnectDeadline) {
		_ = c.recoveryFailure(ctx, op, "transaction files disagree or original recovery deadline expired")
		return false
	}
	// Persist intent before RPC: a lost ACK causes evidence probes, not repetition.
	stored.Metadata["transaction_recovery_dispatched"] = true
	stored.Metadata["transaction_recovery_action"] = action
	stored.Metadata["transaction_phase"] = evidence.Phase
	if err := operationapp.Save(ctx, c.repo.db, c.repo.dialect, stored); err != nil {
		return false
	}
	request := &nodev1.ServiceUpdateRequest{OperationId: op.ID, Action: action, Version: target, Channel: op.RequestedChannel}
	if restore {
		request.BackupIdentity = backup
	} else {
		payload, err := json.Marshal(op.ResolvedTarget)
		if err != nil {
			return false
		}
		request.ResolvedBuildJson = string(payload)
	}
	_, err = c.sendFencedServiceUpdate(ctx, client, op.ID, request)
	// Side effects may have committed despite transport loss; the next step only
	// observes fresh runtime evidence, using the original deadline.
	return !errors.Is(err, operationapp.ErrLeaseLost)
}
