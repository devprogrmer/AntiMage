package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyScopeAllowlist(t *testing.T) {
	for _, tc := range []struct {
		scope, method, path string
		want                bool
	}{
		{"full", http.MethodPost, "/api/settings/backup/import", true},
		{"users_read", http.MethodGet, "/api/v2/users", true},
		{"users_read", http.MethodGet, "/api/v2/users/alice", true},
		{"users_read", http.MethodGet, "/api/users/usage", true},
		{"users_read", http.MethodPost, "/api/v2/users", false},
		{"users_read", http.MethodDelete, "/api/v2/users/alice", false},
		{"users_read", http.MethodGet, "/api/settings/backup/export", false},
		{"users_read", http.MethodGet, "/api/myaccount/api-keys", false},
		{"users_read", http.MethodGet, "/api/v2/users/../settings", false},
		{"unrecognized", http.MethodGet, "/api/v2/users", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := apiKeyScopeAllows(tc.scope, r); got != tc.want {
			t.Errorf("scope=%q %s %s: got %v, want %v", tc.scope, tc.method, tc.path, got, tc.want)
		}
	}
}
