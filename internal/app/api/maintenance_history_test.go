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
		`CREATE TABLE diagnostics (id TEXT PRIMARY KEY, source TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, severity TEXT NOT NULL, code TEXT NOT NULL, summary TEXT NOT NULL, detail TEXT NOT NULL, first_seen_at BIGINT NOT NULL, last_seen_at BIGINT NOT NULL, occurrence_count BIGINT NOT NULL, status TEXT NOT NULL, recommended_action TEXT NOT NULL, UNIQUE(source,resource_type,resource_id,code))`,
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,requested_by TEXT NOT NULL,request_id TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,progress INTEGER NULL,created_at BIGINT NOT NULL,started_at BIGINT NULL,updated_at BIGINT NOT NULL,completed_at BIGINT NULL,error TEXT NOT NULL,metadata_json TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
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
		CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Metadata: map[string]any{"version": "v1.2.3"},
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
}
