package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOperationalCLIUsesAuthenticatedServiceAndNeverFallsBackOnConflict(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/node/3/service/restart" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer isolated-test-token" || r.Header.Get("X-AntiMage-Origin") != "cli" {
			t.Errorf("wrong service request")
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"password=do-not-echo"}`))
	}))
	defer server.Close()
	t.Setenv("ANTIMAGE_CLI_API_URL", server.URL)
	t.Setenv("ANTIMAGE_CLI_API_TOKEN", "isolated-test-token")
	c := &cli{}
	err := c.requestMaintenance([]string{"node-restart", "--node-id", "3"})
	if err == nil || !strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "password") || calls != 1 {
		t.Fatalf("conflict bypass or leaked error: %v calls=%d", err, calls)
	}
}

func TestMaintenanceCLIPrintsNestedOperationIdentityOnly(t *testing.T) {
	result := maintenanceResponseIdentity(map[string]any{"operation": map[string]any{"id": "panel-op", "phase": "queued", "error": "private-response", "token": "private-response"}})
	if len(result) != 2 || result["id"] != "panel-op" || result["phase"] != "queued" {
		t.Fatalf("unexpected identity projection: %+v", result)
	}
	result = maintenanceResponseIdentity(map[string]any{"id": "op\nAuthorization: Bearer private-response", "phase": "queued"})
	if len(result) != 1 || result["phase"] != "queued" {
		t.Fatalf("untrusted multiline identity printed: %+v", result)
	}
}

func TestMaintenanceCLIRoutesMatchServicePaths(t *testing.T) {
	cases := map[string]string{"node-restart": "service/restart", "node-rollback": "service/rollback", "core-update": "xray/update", "geo-update": "geo/update", "core-restart": "restart", "sync-config": "sync", "runtime-stop": "stop"}
	for action, suffix := range cases {
		t.Run(action, func(t *testing.T) {
			path, _, err := maintenanceRequest([]string{action, "--node-id", "3", "--confirm", "--source-operation-id", "source"})
			if err != nil || path != "/api/node/3/"+suffix {
				t.Fatalf("%s %v", path, err)
			}
		})
	}
	if _, _, err := maintenanceRequest([]string{"node-rollback", "--node-id", "3"}); err == nil {
		t.Fatal("rollback confirmation bypassed")
	}
}

func TestPanelCLIFenceChecksDatabaseOwnerAndCommand(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ops.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ddls := []string{`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT,target_type TEXT,target_id TEXT,requested_by TEXT,request_id TEXT,state TEXT,phase TEXT,progress INTEGER,created_at BIGINT,started_at BIGINT,updated_at BIGINT,completed_at BIGINT,error TEXT,metadata_json TEXT)`, operationapp.ExecutorLeaseDDL, operationapp.ResourceFenceDDL, `CREATE TABLE operation_locks(target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`, `CREATE TABLE operation_events(operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT,payload_json TEXT,PRIMARY KEY(operation_id,sequence))`}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	op := operationapp.Operation{ID: "panel-cli-op", Type: "update", TargetType: "panel", TargetID: "panel", State: "running", Phase: "installing", CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Metadata: map[string]any{"origin": "cli"}}
	if err := operationapp.CreateExclusive(ctx, db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	lease, err := operationapp.AcquireExecutorLease(ctx, db, "sqlite", op.ID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(op.ID + "|update"))
	args := []string{"--operation-id", op.ID, "--executor-id", "first", "--lease-generation", fmt.Sprint(lease.Generation), "--resource-generation", fmt.Sprint(lease.ResourceGeneration), "--command-id", fmt.Sprintf("panel-command-%x", digest[:16]), "--action", "update"}
	c := &cli{db: db, dialect: "sqlite"}
	if err := c.checkMaintenanceFence(args); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
		t.Fatal(err)
	}
	next, err := operationapp.AcquireExecutorLease(ctx, db, "sqlite", op.ID, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.ResourceGeneration <= lease.ResourceGeneration || c.checkMaintenanceFence(args) == nil {
		t.Fatal("old CLI command passed newer executor")
	}
	oldCtx := operationapp.WithExecutorLease(ctx, lease)
	op.Phase = "completed"
	op.State = "completed"
	if err := operationapp.Save(oldCtx, db, "sqlite", op); err == nil {
		t.Fatal("stale CLI result changed newer operation")
	}
}
