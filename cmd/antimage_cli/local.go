package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	"github.com/antimage/antimage/internal/platform/requestctx"
)

// localAdminIdentity is the audit actor recorded for host actions taken from
// the local terminal. Authorization for these commands comes from the POSIX
// root/effective-user check, not from a panel token or session.
const localAdminIdentity = "root"

// newLocalMaintenanceService builds the shared fenced operation service. It is
// a variable so tests can inject a fake runtime detector and command scheduler
// without requiring systemd or a live AntiMage installation.
var newLocalMaintenanceService = func(db *sql.DB, dialect string) *systemapp.MaintenanceService {
	return systemapp.NewMaintenanceServiceWithDepsDB(db, dialect, systemapp.DefaultRuntimeDetector{}, systemapp.NewGitHubUpdateChecker(), systemapp.DefaultCommandScheduler{})
}

// localEUID is a seam so tests can exercise both the root and non-root paths
// without requiring the test runner itself to be privileged.
var localEUID = os.Geteuid

// requireLocalRoot enforces that destructive host commands are only executed by
// the local superuser. Non-root callers are rejected before any state changes.
func requireLocalRoot() error {
	if localEUID() != 0 {
		return fmt.Errorf("this command must be run as root")
	}
	return nil
}

// localOperationContext carries the audit identity and origin for host actions
// so they are recorded in the same operations journal as panel actions.
func localOperationContext() context.Context {
	ctx := requestctx.WithAdmin(context.Background(), localAdminIdentity)
	ctx = requestctx.WithID(ctx, newLocalRequestID())
	return operationapp.WithAuditOrigin(ctx, "cli")
}

func newLocalRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "cli-local"
	}
	encoded := hex.EncodeToString(raw[:])
	return strings.Join([]string{encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:]}, "-")
}

// runLocalStatus reports the current runtime and latest maintenance operation.
// It is read-only and therefore available without root.
func (c *cli) runLocalStatus() error {
	service := newLocalMaintenanceService(c.db, c.dialect)
	runtimeInfo := service.Runtime.Info()
	status := service.Status()
	fmt.Printf("Mode:     %s\n", firstOrDash(runtimeInfo.Mode))
	fmt.Printf("Service:  %s\n", firstOrDash(runtimeInfo.Service))
	fmt.Printf("Version:  %s\n", firstOrDash(runtimeInfo.RunningVersion))
	if status.ID == "" {
		fmt.Println("Operation: none")
		return nil
	}
	fmt.Printf("Operation: %s (%s) phase=%s running=%t\n", status.ID, status.Action, status.Phase, status.Running)
	if strings.TrimSpace(status.Message) != "" {
		fmt.Printf("Message:   %s\n", status.Message)
	}
	return nil
}

// runLocalDiagnostics prints the most recent maintenance history as an offline
// diagnostic aid for local operators.
func (c *cli) runLocalDiagnostics() error {
	service := newLocalMaintenanceService(c.db, c.dialect)
	history, err := service.History(10)
	if err != nil {
		return err
	}
	if len(history) == 0 {
		fmt.Println("No maintenance operations recorded.")
		return nil
	}
	for _, op := range history {
		fmt.Printf("%s\t%s\t%s\trunning=%t\n", op.ID, op.Action, op.Phase, op.Running)
	}
	return nil
}

// runLocalLifecycle runs a start/stop/restart through the shared
// MaintenanceService so local root and the panel share one safe operation layer.
func (c *cli) runLocalLifecycle(action string) error {
	if err := requireLocalRoot(); err != nil {
		return err
	}
	service := newLocalMaintenanceService(c.db, c.dialect)
	ctx := localOperationContext()
	var (
		snapshot systemapp.MaintenanceOperationSnapshot
		err      error
	)
	switch action {
	case "start":
		snapshot, err = service.Start(ctx)
	case "stop":
		snapshot, err = service.Stop(ctx)
	case "restart":
		snapshot, err = service.Restart(ctx)
	default:
		return fmt.Errorf("unsupported local lifecycle action %q", action)
	}
	if err != nil {
		return err
	}
	fmt.Printf("operation=%s action=%s phase=%s\n", snapshot.ID, snapshot.Action, snapshot.Phase)
	return nil
}

func firstOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
