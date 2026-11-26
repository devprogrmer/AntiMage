package api

import (
	"net/http"
	"net/netip"
)

// trustedProxyHeaders is installed before authentication and subscription
// routing so all consumers receive the same forwarding trust boundary.
func (s *Server) trustedProxyHeaders(next http.Handler) http.Handler {
	prefixes := make([]netip.Prefix, 0, len(s.cfg.TrustedProxyCIDRs))
	for _, raw := range s.cfg.TrustedProxyCIDRs {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddr(requestPeer(r))
		trusted := err == nil && peer.IsLoopback()
		for _, prefix := range prefixes {
			trusted = trusted || prefix.Contains(peer.Unmap())
		}
		if !trusted {
			r = r.Clone(r.Context())
			for _, name := range []string{
				"Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto",
				"X-Forwarded-Port", "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP",
			} {
				r.Header.Del(name)
			}
		}
		next.ServeHTTP(w, r)
	})
}
