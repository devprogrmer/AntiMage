package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/antimage/antimage/internal/platform/requestctx"
	"net/http"
	"strings"
)

func requestIDFromContext(ctx context.Context) string {
	return requestctx.ID(ctx)
}

func validRequestID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "req-unknown"
	}
	encoded := hex.EncodeToString(bytes[:])
	return strings.Join([]string{encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:]}, "-")
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := requestctx.WithID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
