package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNetworkHistoryScopeFilteringAndOldNode(t *testing.T) {
	raw := json.RawMessage(`{"history":{"storage":"volatile","collection":"on_telemetry_read","truncated":true,"points":[{"node_id":7,"user_id":42,"ack_rtt_ms":50},{"node_id":8,"user_id":42},{"node_id":7,"user_id":43}]}}`)
	got, err := filterNetworkIntelligenceHistory(raw, 7, 42)
	if err != nil || !got.Available || !got.Truncated || len(got.Points) != 1 || got.Storage != "volatile" {
		t.Fatal(got, err)
	}
	got, err = filterNetworkIntelligenceHistory(json.RawMessage(`{"available":false}`), 7, 42)
	if err != nil || got.Available || len(got.Points) != 0 {
		t.Fatal(got, err)
	}
}

func TestNetworkHistoryRequiresFullAccessAndValidScope(t *testing.T) {
	for _, tc := range []struct {
		role, query string
		want        int
	}{
		{"sudo", "node_id=7&user_id=42", http.StatusForbidden},
		{"standard", "node_id=7&user_id=42", http.StatusForbidden},
		{"full_access", "node_id=7", http.StatusBadRequest},
		{"full_access", "node_id=7&user_id=-1", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/history?"+tc.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), adminContextKey, adminPrincipal{Role: tc.role}))
		w := httptest.NewRecorder()
		(&Server{}).handleNetworkIntelligenceHistory(w, r)
		if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Header())
		}
	}
}
