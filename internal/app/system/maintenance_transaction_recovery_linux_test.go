//go:build linux

package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type interruptedTransactionScheduler struct {
	calls [][]string
}

func TestPanelControllerRestoreMatrixKeepsOriginalReservation(t *testing.T) {
	for _, phase := range []string{"rollback_prepared", "backup_verified", "restore_ready", "restore_committed", "rollback_restart_required"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, "panel")
			fixture.installActual(t, fixture.targetBytes)
			manifest, err := os.ReadFile(filepath.Join(fixture.app, ".update-backups", fixture.op, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(manifest)
			writeFixtureJSON(t, filepath.Join(fixture.app, ".update-backups", fixture.op, "restore-state.json"), map[string]any{"identity": fixture.op, "manifest_sha256": hex.EncodeToString(digest[:]), "phase": phase, "deadline_at_unix_nanos": time.Now().Add(time.Minute).UnixNano()})
			committed := phase == "restore_committed" || phase == "rollback_restart_required"
			if committed {
				fixture.installActual(t, fixture.old)
			}
			t.Setenv("ANTIMAGE_APP_DIR", fixture.app)
			db := newPanelUpdateDB(t)
			store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
			now := time.Now()
			store.latest = MaintenanceOperationSnapshot{ID: fixture.op, TargetType: "panel", Action: "rollback", Phase: "outcome_unknown", Running: true, StartedAt: now.Unix(), StartedAtNanos: now.UnixNano(), UpdatedAt: now.Unix(), DesiredVersion: "v1.0.0", TargetPreviousVersion: "v1.0.0", PreviousVersion: "v2.0.0", BackupIdentity: fixture.op}
			store.persistLocked()
			if err := store.PersistenceError(); err != nil {
				t.Fatal(err)
			}
			scheduler := &interruptedTransactionScheduler{}
			detector := &mutableMaintenanceRuntime{info: RuntimeInfo{Service: fixture.target, RunningVersion: "v2.0.0", ProcessStartedAt: now.Add(-time.Minute).UnixNano()}}
			service := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
			service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
			service.recoveryPending = true
			for i := 0; i < 3; i++ {
				_ = service.Status()
			}
			expected := "resume-panel-restore"
			if committed {
				expected = "resume-panel-restart"
			}
			if len(scheduler.calls) != 1 || scheduler.calls[0][0] != expected {
				t.Fatalf("restore replay or incorrect missing step: %+v", scheduler.calls)
			}
			persisted := NewMaintenanceOperationStoreWithDB(db, "sqlite").Latest()
			if !persisted.Running || persisted.StartedAtNanos != now.UnixNano() || !persisted.TransactionRecoveryDispatched {
				t.Fatalf("lost original ownership/deadline: %+v", persisted)
			}
		})
	}
}

func (s *interruptedTransactionScheduler) Schedule(args []string) error {
	s.calls = append(s.calls, append([]string(nil), args...))
	return nil
}

func (s *interruptedTransactionScheduler) ScheduleWithProgressContext(_ context.Context, args []string, _ func(string), done func(error)) error {
	_ = s.Schedule(args)
	// Model a committed/possibly dispatched subprocess whose acknowledgement is
	// lost. The controller must persist intent before invoking this scheduler.
	done(errors.New("isolated acknowledgement loss"))
	return nil
}

func TestPanelControllerReconcilesRealTransactionFilesWithoutRepeatingLostACK(t *testing.T) {
	for _, phase := range []string{"artifact_ready", "backup_verified", "replacement_ready", "replacement_committed", "runtime_restart_required"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, "panel")
			fixture.state.Phase = phase
			fixture.persist(t)
			if phase == "replacement_committed" || phase == "runtime_restart_required" {
				fixture.installActual(t, fixture.targetBytes)
			}
			t.Setenv("ANTIMAGE_APP_DIR", fixture.app)
			db := newPanelUpdateDB(t)
			store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
			now := time.Now()
			store.latest = MaintenanceOperationSnapshot{ID: fixture.op, TargetType: "panel", Action: "update", Phase: "outcome_unknown", Running: true, StartedAt: now.Unix(), StartedAtNanos: now.UnixNano(), UpdatedAt: now.Unix(), DesiredVersion: "v2.0.0", RequestedVersion: "v2.0.0", PreviousVersion: "v1.0.0", ResolvedTarget: &fixture.archive, BackupIdentity: fixture.op}
			store.persistLocked()
			if err := store.PersistenceError(); err != nil {
				t.Fatal(err)
			}
			scheduler := &interruptedTransactionScheduler{}
			detector := &mutableMaintenanceRuntime{info: RuntimeInfo{Service: fixture.target, RunningVersion: "v1.0.0", ProcessStartedAt: now.Add(-time.Minute).UnixNano()}}
			service := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
			service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
			service.recoveryPending = true
			for i := 0; i < 3; i++ {
				_ = service.Status()
			}
			if len(scheduler.calls) != 1 {
				t.Fatalf("lost ACK caused repeated dispatch: %+v", scheduler.calls)
			}
			expected := "resume-panel-install"
			if phase == "replacement_committed" || phase == "runtime_restart_required" {
				expected = "resume-panel-restart"
			}
			if scheduler.calls[0][0] != expected {
				t.Fatalf("wrong missing step: %+v", scheduler.calls)
			}
			persisted := NewMaintenanceOperationStoreWithDB(db, "sqlite").Latest()
			if !persisted.Running || !persisted.TransactionRecoveryDispatched || persisted.TransactionRecoveryAction != expected {
				t.Fatalf("recovery intent not durable: %+v", persisted)
			}
			var locks int
			if err := db.QueryRow(`SELECT COUNT(*) FROM operation_locks WHERE operation_id=?`, fixture.op).Scan(&locks); err != nil || locks != 1 {
				t.Fatalf("unknown outcome lost reservation: %d %v", locks, err)
			}
		})
	}
}
