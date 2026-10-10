package operations_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"github.com/go-sql-driver/mysql"
)

// Uses independent pools and real server transactions, with the production
// migrations. CI must set the URL for both MySQL and MariaDB service jobs.
func TestExternalDatabaseCrossOperationFencing(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("ANTIMAGE_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("ANTIMAGE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	parsed, err := neturl.Parse(url)
	if err != nil {
		t.Fatal("invalid external database URL")
	}
	if !strings.HasPrefix(parsed.Scheme, "mysql") && !strings.HasPrefix(parsed.Scheme, "mariadb") {
		t.Skip("requires a real MySQL or MariaDB server")
	}
	password, _ := parsed.User.Password()
	config := mysql.NewConfig()
	config.User, config.Passwd, config.Net, config.Addr, config.DBName = parsed.User.Username(), password, "tcp", parsed.Host, strings.TrimPrefix(parsed.Path, "/")
	config.ParseTime = true
	config.Timeout = 10 * time.Second
	config.ReadTimeout = 30 * time.Second
	config.WriteTimeout = 30 * time.Second
	config.Params = map[string]string{"charset": "utf8mb4", "collation": "utf8mb4_unicode_ci"}
	open := func() (*sql.DB, error) {
		database, err := sql.Open("mysql", config.FormatDSN())
		if err == nil {
			database.SetMaxOpenConns(1)
		}
		return database, err
	}
	aDB, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer aDB.Close()
	bDB, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer bDB.Close()
	a := struct {
		DB      *sql.DB
		Dialect string
	}{aDB, "mysql"}
	b := struct {
		DB      *sql.DB
		Dialect string
	}{bDB, "mysql"}
	var connectionA, connectionB int64
	if err := a.DB.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&connectionA); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&connectionB); err != nil {
		t.Fatal(err)
	}
	if connectionA == connectionB {
		t.Fatal("independent server sessions were not created")
	}
	if err := migrations.RunMigrations(ctx, a.DB, a.Dialect); err != nil {
		t.Fatal(err)
	}
	if a.Dialect == "mysql" {
		var version string
		if err := a.DB.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		t.Logf("real server: %s", version)
	}
	for _, kind := range []string{"node_update", "node_restart", "core_update", "sync_config", "node_rollback"} {
		t.Run(kind, func(t *testing.T) {
			target := fmt.Sprintf("external-%d", time.Now().UnixNano())
			makeOp := func(id, kind string) operationapp.Operation {
				origin := "api"
				if kind == "node_restart" {
					origin = "cli"
				}
				return operationapp.Operation{ID: id, Type: kind, TargetType: "node", TargetID: target, RequestedBy: "test-controller", RequestID: "external-fencing", State: "queued", Phase: "queued", CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Metadata: map[string]any{"origin": origin}}
			}
			ops := []operationapp.Operation{makeOp(target+"-a", "node_update"), makeOp(target+"-b", kind)}
			t.Cleanup(func() {
				for _, op := range ops {
					for _, table := range []string{"operation_events", "operation_executor_leases"} {
						_, _ = a.DB.Exec("DELETE FROM "+table+" WHERE operation_id=?", op.ID)
					}
					_, _ = a.DB.Exec(`DELETE FROM operations WHERE id=?`, op.ID)
				}
				_, _ = a.DB.Exec(`DELETE FROM operation_locks WHERE target_type='node' AND target_id=?`, target)
				_, _ = a.DB.Exec(`DELETE FROM operation_resource_fences WHERE target_type='node' AND target_id=?`, target)
			})
			type result struct {
				index int
				err   error
			}
			start, results := make(chan struct{}), make(chan result, 2)
			for index, db := range []*sql.DB{a.DB, b.DB} {
				go func(index int, db *sql.DB) {
					<-start
					results <- result{index, operationapp.CreateExclusive(ctx, db, a.Dialect, ops[index])}
				}(index, db)
			}
			close(start)
			winner, wins := -1, 0
			for range 2 {
				result := <-results
				if result.err == nil {
					winner = result.index
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("simultaneous resource acquisitions: %d winners", wins)
			}
			op := ops[winner]
			first, err := operationapp.AcquireExecutorLease(ctx, a.DB, a.Dialect, op.ID, "controller-a", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			app := t.TempDir()
			guard := func(mode string, lease operationapp.ExecutorLease, command string) error {
				if runtime.GOOS != "linux" {
					return nil
				}
				helper, err := filepath.Abs("../../../scripts/antimage/node-command-fence.sh")
				if err != nil {
					return err
				}
				output, err := exec.CommandContext(ctx, "bash", "-c", `source "$1"; APP_DIR="$2"; FENCE_OPERATION_ID="$3"; LEASE_GENERATION="$4"; COMMAND_ID="$5"; RESOURCE_GENERATION="$6"; RESOURCE_ID="$7"; node_command_fence "$8"`, "test", helper, app, lease.OperationID, fmt.Sprint(lease.Generation), command, fmt.Sprint(lease.ResourceGeneration), lease.TargetID, mode).CombinedOutput()
				if err != nil {
					return fmt.Errorf("%w: %s", err, output)
				}
				return nil
			}
			if err := guard("accept", first, "old-command"); err != nil {
				t.Fatal(err)
			}
			if _, err := operationapp.AcquireExecutorLease(ctx, b.DB, b.Dialect, op.ID, "controller-b", time.Minute); !errors.Is(err, operationapp.ErrLeaseHeld) {
				t.Fatalf("live lease takeover: %v", err)
			}
			if _, err := a.DB.ExecContext(ctx, `UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
				t.Fatal(err)
			}
			second, err := operationapp.AcquireExecutorLease(ctx, b.DB, b.Dialect, op.ID, "controller-b", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if second.Generation != first.Generation+1 || second.ResourceGeneration != first.ResourceGeneration+1 {
				t.Fatalf("takeover did not advance both generations: %+v %+v", first, second)
			}
			if err := guard("accept", second, "new-command"); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "linux" && guard("check", first, "old-command") == nil {
				t.Fatal("stale Node CLI command survived real database takeover")
			}
			for index, commandID := range []string{"command-ABC", "command-abc", "command-ABC"} {
				database := []*sql.DB{a.DB, b.DB, a.DB}[index]
				auditCtx := operationapp.WithAuditOrigin(operationapp.WithExecutorLease(ctx, second), "recovery")
				if err := operationapp.AppendAudit(auditCtx, database, op.ID, "fencing_rejected", map[string]any{"command_id": commandID, "token": "private-audit-marker"}); err != nil {
					t.Fatal(err)
				}
			}
			var auditCount int
			if err := b.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_events WHERE operation_id=? AND event_type='fencing.rejected'`, op.ID).Scan(&auditCount); err != nil || auditCount != 2 {
				t.Fatalf("cross-session exact audit deduplication: count=%d err=%v", auditCount, err)
			}
			op.State, op.Phase = "completed", "completed"
			if err := operationapp.Save(operationapp.WithExecutorLease(ctx, first), a.DB, a.Dialect, op); !errors.Is(err, operationapp.ErrLeaseLost) {
				t.Fatalf("stale success persisted: %v", err)
			}
			if err := operationapp.Save(operationapp.WithExecutorLease(ctx, second), b.DB, b.Dialect, op); err != nil {
				t.Fatal(err)
			}
			op = ops[1-winner]
			if err := operationapp.CreateExclusive(ctx, a.DB, a.Dialect, op); err != nil {
				t.Fatal(err)
			}
			third, err := operationapp.AcquireExecutorLease(ctx, a.DB, a.Dialect, op.ID, "controller-c", time.Minute)
			if err != nil || third.ResourceGeneration != second.ResourceGeneration+1 {
				t.Fatalf("cross-operation generation reset: %+v %v", third, err)
			}
		})
	}
}
