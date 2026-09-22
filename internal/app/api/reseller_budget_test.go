package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	_ "modernc.org/sqlite"
)

func TestResellerChildGrantsAreBoundedAndNotRefundedOnDelete(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "reseller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.RunMigrations(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	hash, err := adminapp.HashPassword("pass123")
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admins (id, username, hashed_password, role, permissions, status, subscription_settings) VALUES (1, 'root-admin', ?, 'full_access', ?, 'active', '{}')`, hash, string(permissions)); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: db, dialect: "sqlite", adminRepo: adminapp.NewRepository(db, "sqlite")}
	request := func(method, path, actorName, body string, want int) {
		t.Helper()
		actor, found, err := server.adminRepo.AdminByUsername(context.Background(), actorName)
		if err != nil || !found {
			t.Fatalf("load actor %s: found=%v err=%v", actorName, found, err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), adminContextKey, adminPrincipal{ID: actor.ID, Username: actor.Username, Role: string(actor.Role), Context: adminapp.EffectiveAdminContext{Admin: actor, Source: adminapp.AuthSourceSession}}))
		rec := httptest.NewRecorder()
		switch method {
		case http.MethodPost:
			if strings.HasPrefix(path, "/api/admin/usage/reset/") {
				server.handleAdminUsageResetPath(rec, req)
			} else {
				server.handleCreateAdmin(rec, req)
			}
		case http.MethodDelete:
			server.handleDeleteAdmin(rec, req, strings.TrimPrefix(path, "/api/admin/"))
		case http.MethodPut:
			server.handleUpdateAdmin(rec, req, strings.TrimPrefix(path, "/api/admin/"))
		}
		if rec.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
		}
	}
	request(http.MethodPost, "/api/admin", "root-admin", `{"username":"reseller","password":"secret1","role":"reseller","data_limit":2000}`, http.StatusOK)
	request(http.MethodPost, "/api/admin", "reseller", `{"username":"child1","password":"secret1","role":"standard","data_limit":250,"permissions":{"users":{"delete":true}}}`, http.StatusOK)
	request(http.MethodPost, "/api/admin", "reseller", `{"username":"child2","password":"secret1","role":"standard","data_limit":1749}`, http.StatusOK)
	var allocated int64
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE username = 'reseller'`).Scan(&allocated); err != nil || allocated != 1999 {
		t.Fatalf("reseller allocated=%d err=%v, want 1999", allocated, err)
	}
	request(http.MethodPost, "/api/admin", "reseller", `{"username":"excess","password":"secret1","role":"standard","data_limit":2}`, http.StatusForbidden)
	request(http.MethodDelete, "/api/admin/child1", "reseller", `{}`, http.StatusOK)
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE username = 'reseller'`).Scan(&allocated); err != nil || allocated != 1999 {
		t.Fatalf("deleted child refunded reseller budget: allocated=%d err=%v", allocated, err)
	}
	request(http.MethodPost, "/api/admin", "reseller", `{"username":"still-excess","password":"secret1","role":"standard","data_limit":2}`, http.StatusForbidden)
	var role, mode string
	var limit sql.NullInt64
	if err := db.QueryRow(`SELECT role, traffic_limit_mode, data_limit FROM admins WHERE username = 'child2'`).Scan(&role, &mode, &limit); err != nil || role != "standard" || mode != "created_traffic" || !limit.Valid || limit.Int64 != 1749 {
		t.Fatalf("child role=%q mode=%q limit=%v err=%v", role, mode, limit, err)
	}
	request(http.MethodPost, "/api/admin", "reseller", `{"username":"sudo-child","password":"secret1","role":"sudo","data_limit":1}`, http.StatusForbidden)
	request(http.MethodPut, "/api/admin/child2", "reseller", `{"role":"sudo"}`, http.StatusForbidden)
	request(http.MethodPost, "/api/admin/usage/reset/reseller", "root-admin", `{}`, http.StatusForbidden)
	request(http.MethodPost, "/api/admin/usage/reset/child2", "reseller", `{}`, http.StatusForbidden)
	request(http.MethodPut, "/api/admin/child2", "reseller", `{"data_limit":1750}`, http.StatusOK)
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE username = 'reseller'`).Scan(&allocated); err != nil || allocated != 2000 {
		t.Fatalf("increased child budget: allocated=%d err=%v", allocated, err)
	}
	request(http.MethodPut, "/api/admin/child2", "reseller", `{"data_limit":1700}`, http.StatusOK)
	if err := db.QueryRow(`SELECT created_traffic FROM admins WHERE username = 'reseller'`).Scan(&allocated); err != nil || allocated != 2000 {
		t.Fatalf("reduced child budget refunded grant: allocated=%d err=%v", allocated, err)
	}
	request(http.MethodPut, "/api/admin/child2", "root-admin", `{"data_limit":1701}`, http.StatusForbidden)
	request(http.MethodPut, "/api/admin/child2", "child2", `{"data_limit":null}`, http.StatusForbidden)
	request(http.MethodPost, "/api/admin", "child2", `{"username":"grandchild","password":"secret1","role":"standard","data_limit":1}`, http.StatusForbidden)
	request(http.MethodPost, "/api/admin", "root-admin", `{"username":"outside","password":"secret1","role":"standard"}`, http.StatusOK)
	request(http.MethodPut, "/api/admin/outside", "reseller", `{"data_limit":1}`, http.StatusForbidden)
	request(http.MethodDelete, "/api/admin/reseller", "root-admin", `{}`, http.StatusConflict)
	actor, found, err := server.adminRepo.AdminByUsername(context.Background(), "reseller")
	if err != nil || !found {
		t.Fatalf("load reseller for list: found=%v err=%v", found, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admins", nil)
	req = req.WithContext(context.WithValue(req.Context(), adminContextKey, adminPrincipal{ID: actor.ID, Username: actor.Username, Role: string(actor.Role), Context: adminapp.EffectiveAdminContext{Admin: actor, Source: adminapp.AuthSourceSession}}))
	rec := httptest.NewRecorder()
	server.handleAdminsList(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"child2"`) || strings.Contains(rec.Body.String(), `"username":"outside"`) {
		t.Fatalf("reseller list leaked other admins: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
