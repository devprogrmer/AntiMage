package api

import (
	"net/http/httptest"
	"testing"
)

func TestSubscriptionRequestClientIP(t *testing.T) {
	req := httptest.NewRequest(
		"GET",
		"https://panel.example/sub/test",
		nil,
	)

	req.RemoteAddr = "203.0.113.10:54321"

	if got := subscriptionRequestClientIP(req); got != "203.0.113.10" {
		t.Fatalf("remote ip=%q", got)
	}

	req.Header.Set(
		"X-Forwarded-For",
		"198.51.100.20, 10.0.0.1",
	)

	if got := subscriptionRequestClientIP(req); got != "198.51.100.20" {
		t.Fatalf("forwarded ip=%q", got)
	}

	req.Header.Set(
		"CF-Connecting-IP",
		"192.0.2.30",
	)

	if got := subscriptionRequestClientIP(req); got != "192.0.2.30" {
		t.Fatalf("cloudflare ip=%q", got)
	}
}
