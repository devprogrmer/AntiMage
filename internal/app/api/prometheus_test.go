package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusEndpointRequiresDedicatedToken(t *testing.T) {
	server := &Server{prometheusToken: "metrics-only-secret"}
	for _, tc := range []struct {
		name, method, authorization string
		want                        int
	}{
		{"missing token", http.MethodGet, "", http.StatusUnauthorized},
		{"wrong token", http.MethodGet, "Bearer admin-token", http.StatusUnauthorized},
		{"query token ignored", http.MethodGet, "", http.StatusUnauthorized},
		{"wrong method", http.MethodPost, "Bearer metrics-only-secret", http.StatusMethodNotAllowed},
		{"valid token", http.MethodGet, "Bearer metrics-only-secret", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/metrics?token=metrics-only-secret", nil)
			req.Header.Set("Authorization", tc.authorization)
			response := httptest.NewRecorder()
			server.handlePrometheusMetrics(response, req)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
			if tc.want == http.StatusOK {
				if !strings.Contains(response.Body.String(), "antimage_database_up 0") || !strings.Contains(response.Body.String(), "antimage_go_heap_alloc_bytes") {
					t.Fatalf("missing metrics: %s", response.Body.String())
				}
			} else if strings.Contains(response.Body.String(), "antimage_go_heap_alloc_bytes") {
				t.Fatal("metrics leaked to unauthorized caller")
			}
		})
	}
	server.prometheusToken = ""
	response := httptest.NewRecorder()
	server.handlePrometheusMetrics(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled endpoint status = %d", response.Code)
	}
}
