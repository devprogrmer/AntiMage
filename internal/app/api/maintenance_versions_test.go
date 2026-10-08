package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	systemapp "github.com/antimage/antimage/internal/app/system"
)

type versionCatalogChecker struct{ fakeUpdateChecker }

func (versionCatalogChecker) Versions(context.Context, string, string, bool) (systemapp.VersionCatalog, error) {
	return systemapp.VersionCatalog{
		Stable: []systemapp.BuildCatalogEntry{{Version: "v1.2.0", Channel: "stable"}},
		Dev:    []systemapp.BuildCatalogEntry{{Version: "dev-abcdef0", Channel: "dev"}},
	}, nil
}

func TestMaintenanceVersionsRouteValidatesTargetAndReturnsCatalog(t *testing.T) {
	server, db := testAdminServer(t)
	insertMasterAPIAdmin(t, db, 1, "owner", "pass123", adminapp.RoleFullAccess, adminapp.StatusActive)
	server.maintenance = systemapp.NewMaintenanceServiceWithDeps(
		fakeRuntimeDetector{}, versionCatalogChecker{}, &recordingScheduler{},
	)
	token := adminBearerToken(t, server, "owner", "pass123")

	rec := adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/versions?target=panel&refresh=1", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
	var catalog systemapp.VersionCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Stable) != 1 || catalog.Stable[0].Version != "v1.2.0" || len(catalog.Dev) != 1 {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}

	rec = adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/versions?target=other", token, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid target status=%d body=%s", rec.Code, rec.Body.String())
	}
}
