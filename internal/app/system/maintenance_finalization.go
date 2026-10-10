package system

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (s *MaintenanceService) scheduleOwnedMaintenance(id string, args []string) error {
	fenced, err := s.ops.FencedArgs(id, args)
	if err != nil {
		return err
	}
	return s.Commands.Schedule(fenced)
}

func (s *MaintenanceService) finalizePanelUpdate(op MaintenanceOperationSnapshot, running string) {
	args, err := s.ops.FencedArgs(op.ID, []string{"update-commit"})
	if err != nil {
		return
	}
	s.ops.MarkFinalizing(op.ID)
	if s.ops.PersistenceError() != nil {
		return
	}
	scheduler, ok := s.Commands.(ContextProgressCommandScheduler)
	if !ok {
		s.ops.MarkManualRecoveryRequired(op.ID)
		return
	}
	worker, cancel := context.WithDeadline(context.Background(), time.Unix(0, op.StartedAtNanos).Add(3*time.Minute))
	err = scheduler.ScheduleWithProgressContext(worker, args, nil, func(err error) {
		cancel()
		if err != nil {
			s.ops.MarkOutcomeUnknown(op.ID)
			return
		}
		s.ops.MarkVerified(op.ID, running)
	})
	if err != nil {
		cancel()
		s.ops.MarkOutcomeUnknown(op.ID)
	}
}

func panelFinalizationCompleted(id string) bool {
	app := os.Getenv("ANTIMAGE_APP_DIR")
	if app == "" || !backupIdentityPattern.MatchString(id) {
		return false
	}
	var resource struct {
		Owner string `json:"owner_operation_id"`
	}
	root := filepath.Join(app, ".maintenance-fences")
	if readPrivateJSON(app, filepath.Join(root, "resource.json"), &resource) != nil || resource.Owner != id {
		return false
	}
	var journal struct {
		Commands map[string]string `json:"commands"`
	}
	if readPrivateJSON(app, filepath.Join(root, "operation-"+id+".json"), &journal) != nil {
		return false
	}
	digest := sha256.Sum256([]byte(id + "|update-commit"))
	if journal.Commands[fmt.Sprintf("panel-command-%x", digest[:16])] != "completed" {
		return false
	}
	// A completion record cannot override surviving pending rollback metadata.
	if _, err := os.Lstat(filepath.Join(app, ".update-rollback", "identity")); !os.IsNotExist(err) {
		return false
	}
	return true
}
