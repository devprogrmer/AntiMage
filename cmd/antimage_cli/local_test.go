package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/app/migrations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	_ "modernc.org/sqlite"
)

// recordingScheduler captures the host command a service would run so tests can
// prove the local root CLI routes through the shared operation layer without
// touching systemd, the network or a real AntiMage installation.
type recordingScheduler struct {
	args [][]string
}

func (r *recordingScheduler) Schedule(args []string) error {
	r.args = append(r.args, append([]string(nil), args...))
	return nil
}

type stubRuntime struct {
	info systemapp.RuntimeInfo
}

func (s stubRuntime) Info() systemapp.RuntimeInfo { return s.info }

func newLocalCLITestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "ops.sqlite")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrations.RunMigrations(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	return db
}

// withLocalService swaps the production service factory for one whose scheduler
// is captured, keeping the fenced/audited store and preconditions intact.
func withLocalService(t *testing.T, scheduler *recordingScheduler) {
	t.Helper()
	original := newLocalMaintenanceService
	newLocalMaintenanceService = func(db *sql.DB, dialect string) *systemapp.MaintenanceService {
		return systemapp.NewMaintenanceServiceWithDepsDB(db, dialect, stubRuntime{info: systemapp.RuntimeInfo{Mode: "binary", Service: "antimage", RunningVersion: "dev-abc1234"}}, systemapp.NewGitHubUpdateChecker(), scheduler)
	}
	t.Cleanup(func() { newLocalMaintenanceService = original })
}

func TestLocalLifecycleRequiresRoot(t *testing.T) {
	scheduler := &recordingScheduler{}
	withLocalService(t, scheduler)
	originalEUID := localEUID
	localEUID = func() int { return 1000 }
	t.Cleanup(func() { localEUID = originalEUID })

	app := &cli{db: newLocalCLITestDB(t), dialect: "sqlite"}
	err := app.run([]string{"start"})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("non-root start error = %v, want root denial", err)
	}
	if len(scheduler.args) != 0 {
		t.Fatalf("non-root start scheduled a host command: %#v", scheduler.args)
	}
}

func TestLocalLifecycleRunsThroughSharedServiceAsRoot(t *testing.T) {
	scheduler := &recordingScheduler{}
	withLocalService(t, scheduler)
	originalEUID := localEUID
	localEUID = func() int { return 0 }
	t.Cleanup(func() { localEUID = originalEUID })

	db := newLocalCLITestDB(t)
	app := &cli{db: db, dialect: "sqlite"}
	for _, command := range []string{"start", "up", "stop", "down", "restart"} {
		if err := app.run([]string{command}); err != nil {
			t.Fatalf("%s as root: %v", command, err)
		}
	}
	if len(scheduler.args) != 5 {
		t.Fatalf("expected 5 scheduled host commands, got %d: %#v", len(scheduler.args), scheduler.args)
	}
	for i, args := range scheduler.args {
		if len(args) == 0 {
			t.Fatalf("command %d scheduled empty args", i)
		}
	}
	// The audit actor for terminal actions is the local root identity.
	var requestedBy string
	if err := db.QueryRow(`SELECT requested_by FROM operations ORDER BY created_at DESC LIMIT 1`).Scan(&requestedBy); err != nil {
		t.Fatal(err)
	}
	if requestedBy != localAdminIdentity {
		t.Fatalf("audit actor = %q, want %q", requestedBy, localAdminIdentity)
	}
}

func TestLocalStatusAndDiagnosticsAreReadOnly(t *testing.T) {
	scheduler := &recordingScheduler{}
	withLocalService(t, scheduler)
	app := &cli{db: newLocalCLITestDB(t), dialect: "sqlite"}
	if err := app.run([]string{"status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := app.run([]string{"diagnostics"}); err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if len(scheduler.args) != 0 {
		t.Fatalf("read-only commands scheduled host actions: %#v", scheduler.args)
	}
}

func TestLocalLifecycleConflictStillEnforced(t *testing.T) {
	scheduler := &recordingScheduler{}
	withLocalService(t, scheduler)
	originalEUID := localEUID
	localEUID = func() int { return 0 }
	t.Cleanup(func() { localEUID = originalEUID })

	db := newLocalCLITestDB(t)
	app := &cli{db: db, dialect: "sqlite"}
	if err := app.run([]string{"restart"}); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	// A concurrent operation (e.g. a panel action already recorded) must block a
	// second local lifecycle command through the shared conflict check.
	service := newLocalMaintenanceService(db, "sqlite")
	if _, err := service.Restart(localOperationContext()); err == nil {
		t.Fatal("conflicting local restart was accepted; shared lock not enforced")
	}
}
