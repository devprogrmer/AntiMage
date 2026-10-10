package system

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	_ "modernc.org/sqlite"
)

type mutableMaintenanceRuntime struct{ info RuntimeInfo }

func (r *mutableMaintenanceRuntime) Info() RuntimeInfo { return r.info }

type recordingMaintenanceScheduler struct{ calls [][]string }

type captureUpdateChecker struct{ current *string }

func (c *captureUpdateChecker) Status(_ context.Context, _ string, current *string, _ string) UpdateStatus {
	c.current = current
	return UpdateStatus{}
}

func (c *captureUpdateChecker) Versions(context.Context, string, string, bool) (VersionCatalog, error) {
	return VersionCatalog{Stable: []BuildCatalogEntry{{Version: "v1.2.3", Channel: "stable", Commit: strings.Repeat("a", 40), OS: "linux", Architecture: "amd64", Size: 42, SHA256: strings.Repeat("a", 64), DownloadURL: "https://example.test/v1.2.3", ArtifactName: "panel.tar.gz"}}}, nil
}

func (s *recordingMaintenanceScheduler) Schedule(args []string) error {
	s.calls = append(s.calls, append([]string(nil), args...))
	return nil
}

func (s *recordingMaintenanceScheduler) ScheduleWithProgressContext(_ context.Context, args []string, _ func(string), done func(error)) error {
	if err := s.Schedule(args); err != nil {
		return err
	}
	if done != nil {
		done(nil)
	}
	return nil
}

func TestMaintenanceInfoNeverInfersRunningVersionFromInstalledTag(t *testing.T) {
	installed := "v9.9.9"
	checker := &captureUpdateChecker{}
	service := NewMaintenanceServiceWithDeps(&mutableMaintenanceRuntime{info: RuntimeInfo{Tag: &installed, RunningVersion: ""}}, checker, &recordingMaintenanceScheduler{})
	info, err := service.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Panel.RunningVersion != "" {
		t.Fatalf("running version inferred from installed tag: %q", info.Panel.RunningVersion)
	}
	if checker.current != nil {
		t.Fatalf("update checker current version inferred from installed tag: %q", *checker.current)
	}
}

func TestMaintenanceOperationPublishesProgress(t *testing.T) {
	store := NewMaintenanceOperationStore()
	operation := store.Start("update", []string{"update"}, "Preparing update")
	updates, unsubscribe := store.Subscribe()
	defer unsubscribe()

	initial := <-updates
	if initial.Progress == nil || *initial.Progress != 0 {
		t.Fatalf("initial progress = %v, want 0", initial.Progress)
	}

	store.AppendOutput(operation.ID, "35 28.6M 10.2M 0 11.2M 0:00:02 --:--:-- 0:00:02 11.2M")
	download := <-updates
	if download.Progress == nil || *download.Progress != 36 {
		t.Fatalf("download progress = %v, want 36", download.Progress)
	}

	store.AppendOutput(operation.ID, "Installing update")
	installing := <-updates
	if installing.Progress == nil || *installing.Progress != 88 {
		t.Fatalf("installing progress = %v, want 88", installing.Progress)
	}

	store.MarkRestarting(operation.ID, "Restarting")
	restarting := <-updates
	if restarting.Progress == nil || *restarting.Progress != 100 || restarting.Phase != "restarting" {
		t.Fatalf("restart snapshot = %+v, want restarting at 100%%", restarting)
	}
}

func TestRequestedMaintenanceVersionIsExposedToRestartVerifier(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "stable exact", args: []string{"update", "--version", "v1.2.3"}, want: "v1.2.3"},
		{name: "dev exact", args: []string{"update", "--version", "dev-abcdef0"}, want: "dev-abcdef0"},
		{name: "dev latest", args: []string{"update", "--dev"}, want: "dev"},
		{name: "resolved latest", args: []string{"update", "--version", "latest"}, want: "latest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestedMaintenanceVersion("update", test.args); got != test.want {
				t.Fatalf("requested version = %q, want %q", got, test.want)
			}
		})
	}
	if got := requestedMaintenanceVersion("restart", []string{"restart"}); got != "" {
		t.Fatalf("restart requested version = %q, want empty", got)
	}
}

func TestMaintenanceOperationStorePersistsHistoryAcrossRestart(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "panel-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	createTestOperationsTable(t, db)
	defer db.Close()

	first := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	operation := first.Start("update", []string{"update", "--version", "v1.2.3"}, "Preparing update")
	first.AppendOutput(operation.ID, "Downloading panel artifact")
	first.MarkRestarting(operation.ID, "Waiting for panel restart")

	second := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	if got := second.Latest(); got.ID != operation.ID || got.RequestedVersion != "v1.2.3" || got.Phase != "restarting" || !got.Running {
		t.Fatalf("restored operation = %+v", got)
	}
	history, err := second.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != operation.ID || len(history[0].Logs) < 2 {
		t.Fatalf("persisted history = %+v", history)
	}

	second.Finish(operation.ID, nil)
	third := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	completed := third.Latest()
	if completed.Phase != "restarting" || !completed.Running || completed.FinishedAt != nil {
		t.Fatalf("restart-pending snapshot was not persisted: %+v", completed)
	}
	if completed.UpdatedAt <= 0 || time.Unix(completed.UpdatedAt, 0).IsZero() {
		t.Fatal("persisted operation has an invalid updated timestamp")
	}
}

func newPanelUpdateDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "panel-reconcile.db"))
	if err != nil {
		t.Fatal(err)
	}
	createTestOperationsTable(t, db)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createTestOperationsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{operationapp.ExecutorLeaseDDL, operationapp.ResourceFenceDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.Exec(`CREATE TABLE operations (
id TEXT PRIMARY KEY, operation_type TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL,
requested_by TEXT NOT NULL, request_id TEXT NOT NULL, state TEXT NOT NULL, phase TEXT NOT NULL,
progress INTEGER, created_at BIGINT NOT NULL, started_at BIGINT, updated_at BIGINT NOT NULL,
completed_at BIGINT, error TEXT NOT NULL, metadata_json TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`); err != nil {
		t.Fatal(err)
	}
}

func expirePanelTestExecutor(t *testing.T, store *MaintenanceOperationStore) {
	t.Helper()
	store.mu.Lock()
	store.executionLease = nil
	store.mu.Unlock()
	if _, err := store.db.Exec(`UPDATE operation_executor_leases SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
}

func TestPersistedPanelUpdateVerifiesResolvedLatestTarget(t *testing.T) {
	db := newPanelUpdateDB(t)
	store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := store.Start("update", []string{"update", "--version", "latest"}, "Updating", "v1.2.2")
	store.AppendOutput(op.ID, "Resolved AntiMage version: v1.2.3")
	store.MarkRestarting(op.ID, "Waiting for restart")

	runtime := &mutableMaintenanceRuntime{info: RuntimeInfo{RunningVersion: "v1.2.3", Tag: ptr("v1.2.3"), ProcessStartedAt: time.Now().UnixNano()}}
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(runtime, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.reconcilePersistedOperation()
	got := service.Status()
	if got.Phase != "completed" || got.Running || got.ResolvedVersion != "v1.2.3" {
		t.Fatalf("reconciled latest update = %+v", got)
	}
	if len(scheduler.calls) != 1 || scheduler.calls[0][0] != "update-commit" {
		t.Fatalf("verified update backup was not finalized: %#v", scheduler.calls)
	}
}

func TestPersistedPanelUpdateDoesNotAcceptPreUpdateProcess(t *testing.T) {
	db := newPanelUpdateDB(t)
	store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := store.Start("update", []string{"update", "--version", "v1.2.3"}, "Updating", "v1.2.2")
	store.AppendOutput(op.ID, "Resolved AntiMage version: v1.2.3")
	store.MarkRestarting(op.ID, "Waiting for restart")

	runtime := &mutableMaintenanceRuntime{info: RuntimeInfo{
		RunningVersion:   "v1.2.3",
		Tag:              ptr("v1.2.3"),
		ProcessStartedAt: op.StartedAtNanos - int64(time.Second),
	}}
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(runtime, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.reconcilePersistedOperation()

	got := service.Status()
	if got.Phase != "restarting" || !got.Running || got.FinishedAt != nil {
		t.Fatalf("pre-update process was accepted as verified: %+v", got)
	}
	if len(scheduler.calls) != 0 {
		t.Fatalf("pre-update process triggered update commands: %#v", scheduler.calls)
	}
}

func TestPersistedPanelUpdateSchedulesAndVerifiesRollback(t *testing.T) {
	db := newPanelUpdateDB(t)
	store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := store.Start("update", []string{"update", "--version", "v1.2.4"}, "Updating", "v1.2.3")
	store.MarkRestarting(op.ID, "Waiting for restart")
	runtime := &mutableMaintenanceRuntime{info: RuntimeInfo{RunningVersion: "v1.2.3", Tag: ptr("v1.2.3"), ProcessStartedAt: op.StartedAtNanos + int64(time.Second)}}
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(runtime, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.reconcilePersistedOperation()
	rollingBack := service.Status()
	if rollingBack.Phase != "rolling_back" || !rollingBack.Running {
		t.Fatalf("mismatched update did not enter rollback: %+v", rollingBack)
	}
	if len(scheduler.calls) != 1 || scheduler.calls[0][0] != "update-rollback" {
		t.Fatalf("rollback command = %#v", scheduler.calls)
	}

	runtime.info.ProcessStartedAt = time.Now().UnixNano()
	expirePanelTestExecutor(t, service.ops)
	service = NewMaintenanceServiceWithDeps(runtime, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.reconcilePersistedOperation()
	rolledBack := service.Status()
	if rolledBack.Phase != "rolled_back" || rolledBack.Running || rolledBack.RollbackError != "" {
		t.Fatalf("previous version was not verified after restart: %+v", rolledBack)
	}
}

func TestPanelMaintenanceRejectsConflictingOperations(t *testing.T) {
	runtime := &mutableMaintenanceRuntime{info: RuntimeInfo{Mode: "binary", RunningVersion: "v1.2.2"}}
	service := NewMaintenanceServiceWithDeps(runtime, &captureUpdateChecker{}, &recordingMaintenanceScheduler{})
	if _, err := service.Update(context.Background(), MaintenanceUpdateRequest{Channel: "stable", Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restart(context.Background()); err == nil {
		t.Fatal("restart was accepted while panel update was awaiting verification")
	} else {
		var maintenanceErr MaintenanceError
		if !errors.As(err, &maintenanceErr) || maintenanceErr.Status != 409 {
			t.Fatalf("conflict error = %v", err)
		}
	}
}

func TestSplitMaintenanceOutputSeparatesCarriageReturns(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("0%\r35%\r100%\n"))
	scanner.Split(splitMaintenanceOutput)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(lines, ","), "0%,35%,100%"; got != want {
		t.Fatalf("split output = %q, want %q", got, want)
	}
}
