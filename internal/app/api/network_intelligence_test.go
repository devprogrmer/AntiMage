package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/nodecontroller"
)

func TestNetworkIntelligenceRouteIsOptInAndAuthenticated(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		method  string
		want    int
	}{
		{false, http.MethodGet, http.StatusNotFound},
		{true, http.MethodGet, http.StatusUnauthorized},
		{true, http.MethodPost, http.StatusMethodNotAllowed},
	} {
		s := &Server{cfg: Config{NetworkIntelligenceEnabled: tc.enabled}}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, "/api/network-intelligence/presence", nil))
		if w.Code != tc.want {
			t.Errorf("enabled=%t method=%s status=%d want=%d", tc.enabled, tc.method, w.Code, tc.want)
		}
		if tc.method == http.MethodGet && w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("enabled=%t missing no-store", tc.enabled)
		}
	}
}

func TestNetworkIntelligencePresenceUsesExistingRecordsWithoutExposingIPs(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, status TEXT, used_traffic INTEGER, data_limit INTEGER, expire INTEGER, service_id INTEGER);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT, status TEXT);
CREATE TABLE services (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE user_online_ips (node_id INTEGER, user_id INTEGER, protocol TEXT, ip TEXT, last_seen_at DATETIME);
INSERT INTO users (id, username, status) VALUES (42, 'alice', 'active'), (43, 'stale-user', 'active');
INSERT INTO nodes (id, name, status) VALUES (7, 'edge', 'connected');
INSERT INTO user_online_ips VALUES (7, 42, 'xray', '192.0.2.17', CURRENT_TIMESTAMP);
INSERT INTO user_online_ips VALUES (7, 43, 'xray', '192.0.2.18', '2000-01-01 00:00:00');
`); err != nil {
		t.Fatal(err)
	}
	s := &Server{nodeController: nodecontroller.NewController(nodecontroller.NewRepository(db, "sqlite"))}
	for _, tc := range []struct {
		role adminapp.AdminRole
		want int
	}{
		{adminapp.RoleSudo, http.StatusForbidden},
		{adminapp.RoleFullAccess, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/presence", nil)
		r = r.WithContext(context.WithValue(r.Context(), adminContextKey, adminPrincipal{Role: string(tc.role)}))
		w := httptest.NewRecorder()
		s.handleNetworkIntelligencePresence(w, r)
		if w.Code != tc.want {
			t.Fatalf("role=%s status=%d", tc.role, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "192.0.2.") || strings.Contains(body, "stale-user") ||
			strings.Contains(body, `"username":"alice"`) != (tc.want == http.StatusOK) {
			t.Fatalf("unexpected presence response for %s: %s", tc.role, body)
		}
	}
}

func TestNetworkPresenceKeepsNewestObservationPerUserNodeAndProtocol(t *testing.T) {
	now := time.Now().UTC()
	rows := []nodecontroller.UserOnlineIPRecord{
		{UserID: 42, NodeID: 7, Protocol: "wireguard", LastSeenAt: now.Add(-time.Second)},
		{UserID: 42, NodeID: 7, Protocol: "wireguard", LastSeenAt: now},
		{UserID: 42, NodeID: 7, Protocol: "xray", LastSeenAt: now},
		{UserID: 42, NodeID: 8, Protocol: "wireguard", LastSeenAt: now},
	}
	got := networkPresenceRows(rows)
	if len(got) != 3 || !got[0].LastSeenAt.Equal(now) || got[1].Protocol != "xray" || got[2].NodeID != 8 {
		t.Fatalf("presence=%+v", got)
	}
}
