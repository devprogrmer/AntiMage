package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	settingsapp "github.com/antimage/antimage/internal/app/settings"
)

func TestRoutesMatchProtectedGroups(t *testing.T) {
	handler := (&Server{}).Handler()
	paths := []string{
		"/api/admin/foo",
		"/api/admin/usage/reset/seller",
		"/api/myaccount/api-keys/12",
		"/api/core/config/targets/7/mode",
		"/api/core/geo/apply",
		"/api/inbounds/full",
		"/api/inbounds/vless-in",
		"/api/inbounds/vless-in/usage/reset",
		"/api/hosts/certificate-fingerprint",
		"/api/hosts/1/status",
		"/api/settings",
		"/api/settings/telegram",
		"/api/settings/telegram/backup/send",
		"/api/settings/telegram/test",
		"/api/settings/all",
		"/api/panel/xray/outbound-subs",
		"/api/panel/xray/outbound-subs/1/refresh",
		"/api/v2/services/1/users/actions",
		"/api/v2/users/example",
		"/api/user/example/reset",
		"/api/nodes/usage",
		"/api/node/1/restart",
		"/xray/reality-keypair",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected route %s to require auth, got status %d body %q", path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminTokenRouteIsNotCapturedByAdminWildcard(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/token", nil)
	rec := httptest.NewRecorder()

	(&Server{}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected admin token route to reach token handler, got status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestDocsRoutes(t *testing.T) {
	server := &Server{cfg: Config{APIDocsEnabled: true}}
	server.apiDocsEnabled.Store(true)
	handler := server.Handler()

	t.Run("openapi json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body %q", rec.Code, rec.Body.String())
		}
		var spec map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
			t.Fatalf("openapi response is not valid json: %v", err)
		}
		if spec["openapi"] == "" {
			t.Fatalf("openapi version is missing")
		}
	})

	t.Run("legacy docs paths open the native API reference", func(t *testing.T) {
		for _, path := range []string{"/docs", "/docs/", "/docs/index.html"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusMovedPermanently {
				t.Fatalf("expected redirect from %s, got %d", path, rec.Code)
			}
			if location := rec.Header().Get("Location"); location != "/guide/api" {
				t.Fatalf("expected native API reference redirect, got %q", location)
			}
		}
	})
}

func TestDocsRoutesDisabledByDefault(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, path := range []string{"/openapi.json"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected disabled docs route %s to return 404, got %d body %q", path, rec.Code, rec.Body.String())
			}
		})
	}
	for _, path := range []string{"/docs", "/docs/"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/guide/api" {
				t.Fatalf("expected legacy docs redirect, got status %d location %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func TestOpenAPIDocsSettingChangesWithoutRebuildingHandler(t *testing.T) {
	server := &Server{}
	handler := server.Handler()
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
		return rec
	}

	if rec := request(); rec.Code != http.StatusNotFound {
		t.Fatalf("expected docs to start disabled, got %d", rec.Code)
	}

	server.applyRuntimeSettings(settingsapp.RuntimeSettings{APIDocsEnabled: true})
	if rec := request(); rec.Code != http.StatusOK {
		t.Fatalf("expected docs to become enabled after runtime setting update, got %d body %q", rec.Code, rec.Body.String())
	}

	server.applyRuntimeSettings(settingsapp.RuntimeSettings{APIDocsEnabled: false})
	if rec := request(); rec.Code != http.StatusNotFound {
		t.Fatalf("expected docs to become disabled after runtime setting update, got %d", rec.Code)
	}
}

func TestOpenAPIOperationsHaveDeveloperDetails(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openAPIJSON, &spec); err != nil {
		t.Fatalf("openapi json is invalid: %v", err)
	}
	methods := map[string]bool{
		http.MethodGet:    true,
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodDelete: true,
	}
	for path, item := range spec.Paths {
		for method, operation := range item {
			if !methods[strings.ToUpper(method)] {
				continue
			}
			if operation.OperationID == "" {
				t.Fatalf("%s %s is missing operationId", strings.ToUpper(method), path)
			}
			if len(operation.Description) < 120 {
				t.Fatalf("%s %s has a too-short developer description: %q", strings.ToUpper(method), path, operation.Description)
			}
		}
	}
}
