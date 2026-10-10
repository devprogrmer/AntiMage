//go:build cgo

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/diagnostics"
	"github.com/antimage/antimage/internal/app/operations"
)

func TestMaintenanceDiagnosticsAndOperationsRoutes(t *testing.T) {
	server, db := testAdminServer(t)
	insertMasterAPIAdmin(t, db, 1, "owner", "pass123", adminapp.RoleFullAccess, adminapp.StatusActive)
	token := adminBearerToken(t, server, "owner", "pass123")

	for _, statement := range []string{
		`CREATE TABLE operation_events (operation_id TEXT NOT NULL, sequence BIGINT NOT NULL, event_type TEXT NOT NULL, observed_at BIGINT NOT NULL, payload_json TEXT, PRIMARY KEY(operation_id,sequence))`,
		`CREATE TABLE diagnostics (id TEXT PRIMARY KEY, source TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, severity TEXT NOT NULL, code TEXT NOT NULL, summary TEXT NOT NULL, detail TEXT NOT NULL, first_seen_at BIGINT NOT NULL, last_seen_at BIGINT NOT NULL, occurrence_count BIGINT NOT NULL, status TEXT NOT NULL, recommended_action TEXT NOT NULL, UNIQUE(source,resource_type,resource_id,code))`,
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,requested_by TEXT NOT NULL,request_id TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,progress INTEGER NULL,created_at BIGINT NOT NULL,started_at BIGINT NULL,updated_at BIGINT NOT NULL,completed_at BIGINT NULL,error TEXT NOT NULL,metadata_json TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO operation_events VALUES ('op-test-1',1,'command.outcome_reconciled',1,'{"command_id":"command-original","resource_generation":2,"token":"history-secret"}')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	diagnostic, err := (diagnostics.Store{DB: db}).Observe(ctx, diagnostics.Observation{
		Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "disk.pressure",
		Summary: "Disk usage is high", Detail: "Reported disk use is at least 80 percent.", RecommendedAction: "Review disk usage.",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := operations.Upsert(ctx, db, "sqlite", operations.Operation{
		ID: "op-test-1", Type: "panel_update", TargetType: "panel", TargetID: "local",
		RequestedBy: "owner", RequestID: "req-test-1", State: "completed", Phase: "verified",
		CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Error: "Authorization: Bearer history-secret",
		Metadata: map[string]any{"version": "v1.2.3", "snapshot": map[string]any{"desired_version": "v1.2.3", "logs": []string{"history-secret"}, "resolved_target": map[string]any{"download_url": "https://example.test?token=history-secret"}}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/diagnostics?source=runtime", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics status=%d body=%s", rec.Code, rec.Body.String())
	}
	var diagnosticsResponse struct {
		Diagnostics []diagnostics.Record `json:"diagnostics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &diagnosticsResponse); err != nil {
		t.Fatal(err)
	}
	if len(diagnosticsResponse.Diagnostics) != 1 || diagnosticsResponse.Diagnostics[0].ID != diagnostic.ID {
		t.Fatalf("diagnostics response=%+v", diagnosticsResponse)
	}

	rec = adminJSONRequest(t, server, http.MethodPost, "/api/maintenance/diagnostics/"+diagnostic.ID+"/acknowledge", token, "{}")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("acknowledge status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/diagnostics?status=acknowledged", token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"acknowledged"`) {
		t.Fatalf("acknowledged diagnostics status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/operations", token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"request_id":"req-test-1"`) || !strings.Contains(rec.Body.String(), `"phase":"verified"`) {
		t.Fatalf("operations status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "history-secret") || strings.Contains(rec.Body.String(), "download_url") || !strings.Contains(rec.Body.String(), `"desired_version":"v1.2.3"`) {
		t.Fatalf("operation history exposed internal recovery payload or lost version evidence: %s", rec.Body.String())
	}
}
