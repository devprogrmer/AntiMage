package api

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNetworkIntelligenceNodeRejectsUnauthorizedAndInvalidNode(t *testing.T) {
	for _, tc := range []struct {
		role, query string
		status      int
	}{
		{"standard", "1", http.StatusForbidden},
		{"sudo", "1", http.StatusForbidden},
		{"full_access", "-1", http.StatusBadRequest},
		{"full_access", "invalid", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/node?node_id="+tc.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), adminContextKey, adminPrincipal{Role: tc.role}))
		w := httptest.NewRecorder()
		(&Server{}).handleNetworkIntelligenceNode(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("role=%s query=%s status=%d", tc.role, tc.query, w.Code)
		}
	}
}

func TestDiagnosticDurationRejectsInvalidMeasurements(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), -1, 0, 600001} {
		if millisecondsDuration(&value) != nil {
			t.Fatalf("accepted %v", value)
		}
	}
	value := 123.5
	if duration := millisecondsDuration(&value); duration == nil || duration.Microseconds() != 123500 {
		t.Fatal(duration)
	}
}
