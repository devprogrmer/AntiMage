package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithRequestIDGeneratesAndPropagatesIDToErrors(t *testing.T) {
	var contextID string
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = requestIDFromContext(r.Context())
		writeError(w, http.StatusBadRequest, "bad input")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/example", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	gotID := response.Header().Get("X-Request-ID")
	if !validRequestID(gotID) || gotID != contextID {
		t.Fatalf("header request ID %q and context request ID %q should match and be valid", gotID, contextID)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_id"] != gotID {
		t.Fatalf("error request_id = %v, want %q", payload["request_id"], gotID)
	}
}

func TestWithRequestIDAcceptsSafeCallerIDAndRejectsUnsafeID(t *testing.T) {
	for _, test := range []struct{ input, expected string }{{"req-12345678", "req-12345678"}, {"bad value", ""}} {
		response := httptest.NewRecorder()
		handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("X-Request-ID", test.input)
		handler.ServeHTTP(response, request)
		actual := response.Header().Get("X-Request-ID")
		if test.expected != "" && actual != test.expected {
			t.Fatalf("request ID = %q, want %q", actual, test.expected)
		}
		if test.expected == "" && !validRequestID(actual) {
			t.Fatalf("unsafe caller request ID was not replaced: %q", actual)
		}
	}
}
