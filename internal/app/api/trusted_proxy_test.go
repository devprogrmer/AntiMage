package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxyHeadersEnforcePeerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, peer string
		cidrs      []string
		trusted    bool
	}{
		{"untrusted", "192.0.2.1:9000", nil, false},
		{"configured", "192.0.2.1:9000", []string{"192.0.2.0/24"}, true},
		{"ipv6", "[2001:db8::1]:9000", []string{"2001:db8::/32"}, true},
		{"loopback gateway", "127.0.0.1:9000", nil, true},
		{"invalid prefix", "192.0.2.1:9000", []string{"invalid"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: Config{TrustedProxyCIDRs: tc.cidrs}}
			r := httptest.NewRequest(http.MethodPost, "http://internal/api/auth/login", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("Origin", "https://public.example")
			r.Header.Set("X-Forwarded-Host", "public.example")
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-For", "198.51.100.9")
			r.Header.Set("CF-Connecting-IP", "198.51.100.9")
			s.trustedProxyHeaders(http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
				if requestOriginAllowed(got) != tc.trusted || requestIsHTTPS(got) != tc.trusted {
					t.Fatal("forwarding headers bypassed peer boundary")
				}
				if (requestRemote(got) == "198.51.100.9") != tc.trusted {
					t.Fatal("forwarded client address bypassed peer boundary")
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
			if r.Header.Get("X-Forwarded-Host") != "public.example" {
				t.Fatal("middleware mutated caller request")
			}
		})
	}
}
