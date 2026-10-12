//go:build cgo

package api

import (
	adminapp "github.com/antimage/antimage/internal/app/admin"
	"net/http"
	"testing"
)

func TestNodeRollbackAuthorizationAndConfirmation(t *testing.T) {
	server, db := testAdminServer(t)
	insertMasterAPIAdmin(t, db, 1, "owner", "pass123", adminapp.RoleFullAccess, adminapp.StatusActive)
	insertMasterAPIAdmin(t, db, 2, "seller", "pass123", adminapp.RoleStandard, adminapp.StatusActive)
	owner := adminBearerToken(t, server, "owner", "pass123")
	seller := adminBearerToken(t, server, "seller", "pass123")
	for _, item := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/node/1/service/rollback", `{"confirm":true,"source_operation_id":"missing"}`},
		{http.MethodGet, "/api/node/1/service/rollback/backup?source_operation_id=missing", ""},
		{http.MethodPost, "/api/nodes/rollouts", `{"node_ids":[1],"mode":"canary","canary_count":1}`},
		{http.MethodPost, "/api/nodes/rollouts/missing/start", `{"confirm":true}`},
		{http.MethodPost, "/api/nodes/rollouts/missing/cancel", `{}`},
		{http.MethodPost, "/api/nodes/rollouts/missing/retry", `{}`},
	} {
		rec := adminJSONRequest(t, server, item.method, item.path, seller, item.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unauthorized %s: %d %s", item.path, rec.Code, rec.Body.String())
		}
	}
	rec := adminJSONRequest(t, server, http.MethodPost, "/api/node/1/service/rollback", owner, `{"source_operation_id":"missing"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed rollback: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminJSONRequest(t, server, http.MethodPost, "/api/node/1/service/rollback", owner, `{"confirm":true,"source_operation_id":"missing"}`)
	if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
		t.Fatalf("missing backup accepted: %s", rec.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM node_operations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected command queued count=%d err=%v", count, err)
	}
}
