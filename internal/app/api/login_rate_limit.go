package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	loginMaxFailures     = 5
	loginFailureWindow   = 5 * time.Minute
	loginAttemptCapacity = 4096
)

type loginAttempt struct {
	failures int
	resetAt  time.Time
}

type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	now      func() time.Time
}

func loginRateLimitKey(r *http.Request, username string) string {
	return loginRateLimitKeyFromPeer(username, requestPeer(r))
}

func loginRateLimitKeyFromPeer(username, peer string) string {
	remote := strings.TrimSpace(peer)
	if remote == "" {
		remote = "unknown"
	}
	return remote + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func parseTrustedProxyCIDRs(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", value, err)
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func requestPeerFromTrustedProxies(r *http.Request, trusted []netip.Prefix) string {
	if r == nil {
		return ""
	}
	peer := requestPeer(r)
	peerIP, ok := remoteIP(r.RemoteAddr)
	if !ok || !isTrustedProxy(peerIP, trusted) {
		return peer
	}
	forwardedHeader := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwardedHeader == "" {
		for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
			if addr, ok := parseForwardedIP(r.Header.Get(header)); ok {
				return addr.String()
			}
		}
		return peerIP.String()
	}
	forwarded := strings.Split(forwardedHeader, ",")
	current := peerIP
	for index := len(forwarded) - 1; index >= 0; index-- {
		if !isTrustedProxy(current, trusted) {
			break
		}
		next, ok := parseForwardedIP(forwarded[index])
		if !ok {
			return current.String()
		}
		current = next
	}
	return current.String()
}

func remoteIP(remote string) (netip.Addr, bool) {
	remote = strings.TrimSpace(remote)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	addr, err := netip.ParseAddr(strings.Trim(remote, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone(""), true
}

func parseForwardedIP(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	addr, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone(""), true
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) requestPeer(r *http.Request) string {
	return requestPeerFromTrustedProxies(r, s.trustedProxyCIDRs)
}

func (l *loginRateLimiter) allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	attempt, ok := l.attempts[key]
	if !ok {
		l.removeExpired(now)
		return len(l.attempts) < loginAttemptCapacity
	}
	if !now.Before(attempt.resetAt) {
		delete(l.attempts, key)
		return true
	}
	return attempt.failures < loginMaxFailures
}

func (l *loginRateLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	attempt, ok := l.attempts[key]
	if ok && !now.Before(attempt.resetAt) {
		delete(l.attempts, key)
		ok = false
	}
	if !ok && len(l.attempts) >= loginAttemptCapacity {
		l.removeExpired(now)
		if len(l.attempts) >= loginAttemptCapacity {
			return
		}
	}
	attempt.failures++
	attempt.resetAt = now.Add(loginFailureWindow)
	if l.attempts == nil {
		l.attempts = make(map[string]loginAttempt)
	}
	l.attempts[key] = attempt
}

func (l *loginRateLimiter) removeExpired(now time.Time) {
	for key, attempt := range l.attempts {
		if !now.Before(attempt.resetAt) {
			delete(l.attempts, key)
		}
	}
}

func (l *loginRateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

func (l *loginRateLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func writeLoginRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(loginFailureWindow.Seconds())))
	writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
}
