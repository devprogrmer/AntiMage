package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// buildRuntimeConfigBenchDB populates a realistic panel database: many users
// across the proxy protocols, services mapped to inbounds, and a node raw
// config with a handful of inbounds. It exercises the exact query path the
// Controller runs on every node sync.
func buildRuntimeConfigBenchDB(b *testing.B, users int) (*sql.DB, NodeRow) {
	b.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(b.TempDir(), "bench.db")+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, credential_key TEXT, flow TEXT, service_id INTEGER, status TEXT, ip_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (id INTEGER PRIMARY KEY, user_id INTEGER, type TEXT, settings TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled INTEGER DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
CREATE TABLE vpn_user_sessions (id INTEGER PRIMARY KEY, user_id INTEGER, client_ip TEXT, ended_at INTEGER);
CREATE INDEX idx_proxies_user ON proxies(user_id);
`); err != nil {
		b.Fatal(err)
	}
	protocols := []string{"vmess", "vless", "trojan", "shadowsocks", "hysteria"}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < users; i++ {
		service := int64(i/4 + 1)
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, credential_key, flow, service_id, status) VALUES (?, ?, ?, '', ?, 'active')`,
			i+1, fmt.Sprintf("user-%d", i+1), fmt.Sprintf("key-%d", i+1), service); err != nil {
			b.Fatal(err)
		}
		for _, protocol := range protocols {
			// Production panels store per-user proxy overrides only for the
			// minority of users that need them; the rest share the default
			// empty settings object.
			settings := "{}"
			if i%5 == 0 {
				settings = fmt.Sprintf(`{"id":%q,"password":%q}`, fmt.Sprintf("11111111-1111-1111-1111-%012d", i+1), fmt.Sprintf("pw-%d", i+1))
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO proxies (user_id, type, settings) VALUES (?, ?, ?)`, i+1, protocol, settings); err != nil {
				b.Fatal(err)
			}
		}
	}
	services := users/4 + 1
	for s := 1; s <= services; s++ {
		for idx, tag := range []string{"vmess-in", "vless-in", "trojan-in", "ss-in", "hysteria-in"} {
			hostID := s*10 + idx
			if _, err := tx.ExecContext(ctx, `INSERT INTO hosts (id, inbound_tag, is_disabled) VALUES (?, ?, 0)`, hostID, tag); err != nil {
				b.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO service_hosts (service_id, host_id) VALUES (?, ?)`, s, hostID); err != nil {
				b.Fatal(err)
			}
		}
	}
	raw := map[string]any{"inbounds": []any{
		map[string]any{"tag": "vmess-in", "protocol": "vmess", "settings": map[string]any{"clients": []any{}}},
		map[string]any{"tag": "vless-in", "protocol": "vless", "settings": map[string]any{"clients": []any{}}},
		map[string]any{"tag": "trojan-in", "protocol": "trojan", "settings": map[string]any{"clients": []any{}}},
		map[string]any{"tag": "ss-in", "protocol": "shadowsocks", "settings": map[string]any{"clients": []any{}}},
		map[string]any{"tag": "hysteria-in", "protocol": "hysteria", "settings": map[string]any{"clients": []any{}}},
	}}
	encoded, _ := json.Marshal(raw)
	if _, err := tx.ExecContext(ctx, `INSERT INTO xray_config (id, data) VALUES (1, ?)`, string(encoded)); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return db, NodeRow{ID: 7, XrayVersion: "v25.0.0"}
}

func BenchmarkBuildRuntimeConfig(b *testing.B) {
	for _, users := range []int{100, 1000} {
		db, node := buildRuntimeConfigBenchDB(b, users)
		controller := NewController(NewRepository(db, "sqlite"))
		b.Run(fmt.Sprintf("users=%d", users), func(b *testing.B) {
			b.ReportAllocs()
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				if _, err := controller.buildRuntimeConfig(ctx, node); err != nil {
					b.Fatal(err)
				}
			}
		})
		db.Close()
	}
}

func BenchmarkRepositoryRuntimeUsers(b *testing.B) {
	db, _ := buildRuntimeConfigBenchDB(b, 1000)
	defer db.Close()
	repo := NewRepository(db, "sqlite")
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := repo.RuntimeUsers(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
