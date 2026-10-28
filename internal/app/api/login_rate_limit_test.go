package api

import (
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func TestLoginRateLimiterBlocksRepeatedFailures(t *testing.T) {
	now := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	limiter := loginRateLimiter{now: func() time.Time { return now }}

	for range loginMaxFailures {
		limiter.recordFailure("192.0.2.1\x00admin")
	}
	if limiter.allowed("192.0.2.1\x00admin") {
		t.Fatal("expected repeated failures to block login")
	}

	now = now.Add(loginFailureWindow)
	if !limiter.allowed("192.0.2.1\x00admin") {
		t.Fatal("expected limiter to expire")
	}
}

func TestLoginRateLimiterResetsAfterSuccess(t *testing.T) {
	limiter := loginRateLimiter{}
	for range loginMaxFailures - 1 {
		limiter.recordFailure("192.0.2.1\x00admin")
	}
	limiter.reset("192.0.2.1\x00admin")
	if !limiter.allowed("192.0.2.1\x00admin") {
		t.Fatal("expected successful login to clear failures")
	}
}

func TestLoginRateLimitKeyNormalizesUsername(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got, want := loginRateLimitKey(req, " Admin "), "192.0.2.1\x00admin"; got != want {
		t.Fatalf("loginRateLimitKey() = %q, want %q", got, want)
	}
}

func TestLoginRateLimitUsesForwardedIPOnlyFromTrustedProxy(t *testing.T) {
	trusted, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	spoofed := httptest.NewRequest("POST", "/api/auth/login", nil)
	spoofed.RemoteAddr = "198.51.100.9:4321"
	spoofed.Header.Set("X-Forwarded-For", "203.0.113.77")
	if got := requestPeerFromTrustedProxies(spoofed, trusted); got != "198.51.100.9" {
		t.Fatalf("untrusted forwarded header changed peer to %q", got)
	}

	proxied := httptest.NewRequest("POST", "/api/auth/login", nil)
	proxied.RemoteAddr = "10.0.0.2:4321"
	proxied.Header.Set("X-Forwarded-For", "203.0.113.77, 198.51.100.9, 10.0.0.1")
	if got := requestPeerFromTrustedProxies(proxied, trusted); got != "198.51.100.9" {
		t.Fatalf("trusted proxy chain resolved to %q, want nearest untrusted client", got)
	}
	if proxiedKey := loginRateLimitKeyFromPeer("admin", requestPeerFromTrustedProxies(proxied, trusted)); proxiedKey == loginRateLimitKeyFromPeer("admin", requestPeer(proxied)) {
		t.Fatal("trusted forwarded client shares limiter key with proxy transport address")
	}
}

func TestTrustedProxyCIDRValidationAndMalformedForwardedFallback(t *testing.T) {
	if _, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8", "not-a-cidr"}); err == nil {
		t.Fatal("invalid trusted proxy CIDR was accepted")
	}
	trusted, err := parseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.2:4321"
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := requestPeerFromTrustedProxies(req, trusted); got != "10.0.0.2" {
		t.Fatalf("malformed forwarded header fallback = %q, want trusted transport peer", got)
	}
	if !trusted[0].Contains(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("parsed trusted CIDR does not contain an expected proxy address")
	}
	req.Header.Del("X-Forwarded-For")
	req.Header.Set("CF-Connecting-IP", "198.51.100.27")
	if got := requestPeerFromTrustedProxies(req, trusted); got != "198.51.100.27" {
		t.Fatalf("trusted Cloudflare client IP = %q, want 198.51.100.27", got)
	}
}

func TestLoginRateLimiterFailsClosedAtCapacity(t *testing.T) {
	limiter := loginRateLimiter{}
	for i := range loginAttemptCapacity {
		limiter.recordFailure("192.0.2.1\x00user" + strconv.Itoa(i))
	}
	if limiter.allowed("192.0.2.1\x00new-user") {
		t.Fatal("expected a new key to be blocked while the limiter is full")
	}
}
