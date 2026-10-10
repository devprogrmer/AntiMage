package system

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"time"
)

func (s *MaintenanceService) reconcilePanelTransaction(op MaintenanceOperationSnapshot, info RuntimeInfo) bool {
	app := os.Getenv("ANTIMAGE_APP_DIR")
	if s.ops.db == nil || app == "" || (op.Action != "update" && op.Action != "rollback") {
		return true
	}
	restore := op.Action == "rollback" || op.Phase == "rolling_back" || op.TransactionRecoveryAction == "resume-panel-restore"
	backup := op.BackupIdentity
	if backup == "" {
		backup = op.ID
	}
	targetID := info.Service
	if targetID == "" {
		targetID = "antimage"
	}
	evidence, err := InspectBinaryTransaction(app, op.ID, backup, "panel", targetID, runtime.GOARCH, restore, time.Unix(0, op.StartedAtNanos).Add(3*time.Minute).UnixNano())
	if err != nil || !evidence.BackupValid || evidence.NextAction == "manual_recovery_required" {
		s.ops.MarkManualRecoveryRequired(op.ID)
		return false
	}
	target := op.DesiredVersion
	if restore {
		target = op.TargetPreviousVersion
		if target == "" {
			target = op.PreviousVersion
		}
	}
	if evidence.TargetVersion != target || (!restore && (op.ResolvedTarget == nil || evidence.ArtifactSHA256 != op.ResolvedTarget.SHA256 || !evidence.ArtifactValid)) {
		s.ops.MarkManualRecoveryRequired(op.ID)
		return false
	}
	if evidence.FilesMatch && panelVersionMatches(target, info.RunningVersion, stringPtrValue(info.Tag)) && info.ProcessStartedAt > op.StartedAtNanos {
		return true
	}
	if op.TransactionRecoveryDispatched {
		if time.Since(time.Unix(0, op.StartedAtNanos)) > 3*time.Minute {
			s.ops.MarkManualRecoveryRequired(op.ID)
		}
		return false
	}
	if time.Since(time.Unix(0, op.StartedAtNanos)) > 3*time.Minute {
		s.ops.MarkManualRecoveryRequired(op.ID)
		return false
	}
	var args []string
	switch evidence.NextAction {
	case "resume_install":
		if restore || op.ResolvedTarget == nil {
			s.ops.MarkManualRecoveryRequired(op.ID)
			return false
		}
		payload, err := json.Marshal(op.ResolvedTarget)
		if err != nil {
			return false
		}
		args = []string{"resume-panel-install", "--operation-id", op.ID, "--version", target, "--resolved-build", string(payload)}
	case "resume_restore":
		if !restore {
			s.ops.MarkManualRecoveryRequired(op.ID)
			return false
		}
		args = []string{"resume-panel-restore", "--backup-id", backup, "--target-version", target}
	case "restart_only":
		args = []string{"resume-panel-restart"}
	default:
		s.ops.MarkManualRecoveryRequired(op.ID)
		return false
	}
	fenced, err := s.ops.FencedArgs(op.ID, args)
	if err != nil {
		return false
	}
	s.ops.MarkTransactionRecovery(op.ID, args[0])
	if s.ops.PersistenceError() != nil {
		return false
	}
	scheduler, ok := s.Commands.(ContextProgressCommandScheduler)
	if !ok {
		s.ops.MarkManualRecoveryRequired(op.ID)
		return false
	}
	worker, cancel := context.WithDeadline(context.Background(), time.Unix(0, op.StartedAtNanos).Add(3*time.Minute))
	err = scheduler.ScheduleWithProgressContext(worker, fenced, func(line string) { s.ops.AppendOutput(op.ID, line) }, func(err error) {
		cancel()
		if err != nil {
			s.ops.MarkOutcomeUnknown(op.ID)
		} else {
			s.ops.MarkRestarting(op.ID, "Missing transaction step executed; verifying fresh runtime")
		}
	})
	if err != nil {
		cancel()
		s.ops.MarkOutcomeUnknown(op.ID)
	}
	return false
}
