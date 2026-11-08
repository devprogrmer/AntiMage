package xrayconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func inboundListFixture(t testing.TB, count int) Repository {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
		CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT, xray_config_mode TEXT, xray_config TEXT);
		CREATE TABLE inbounds (tag TEXT, uplink BIGINT, downlink BIGINT, usage_coefficient REAL,
			upload_speed_limit BIGINT, download_speed_limit BIGINT);
		INSERT INTO nodes VALUES (2,'connected','default',NULL),(4,'disabled',NULL,NULL)`)
	config := func(protocol string, repeat bool) string {
		inbounds := make([]map[string]any, 0, count+1)
		for i := 0; i < count; i++ {
			inbounds = append(inbounds, map[string]any{
				"tag": fmt.Sprintf("inbound-%03d", i), "protocol": protocol, "port": 8000 + i,
				"settings": map[string]any{"clients": []any{}, "decryption": "none"},
			})
		}
		if repeat {
			inbounds = append(inbounds, inbounds[0])
		}
		raw, err := json.Marshal(map[string]any{"inbounds": inbounds})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	exec(`INSERT INTO xray_config VALUES (1,?)`, config("vless", false))
	for _, id := range []int{3, 5, 6, 10, 11} {
		exec(`INSERT INTO nodes VALUES (?, 'connected', 'custom', ?)`, id, config("trojan", true))
	}
	exec(`INSERT INTO nodes VALUES (8, 'deleted', 'custom', ?)`, config("trojan", false))
	exec(`INSERT INTO nodes VALUES (9, 'connected', 'custom', NULL)`)
	exec(`INSERT INTO inbounds VALUES ('inbound-000', 13, 29, 1.5, 0, 0)`)
	return NewRepository(db, "sqlite", Options{})
}

func TestFullInboundsPreservesDirectAndInheritedTargets(t *testing.T) {
	repo := inboundListFixture(t, 2)
	inbounds, err := repo.FullInbounds(context.Background())
	if err != nil || len(inbounds) != 2 {
		t.Fatalf("inbounds = %v, error = %v", inbounds, err)
	}
	first := inbounds[0]
	if first["tag"] != "inbound-000" || first["protocol"] != "vless" || first["uplink"] != int64(13) {
		t.Fatalf("unexpected inbound: %v", first)
	}
	direct := targetObjects([]string{"master", "node:10", "node:11", "node:3", "node:5", "node:6"})
	effective := targetObjects([]string{"master", "node:10", "node:11", "node:2", "node:3", "node:4", "node:5", "node:6"})
	if !reflect.DeepEqual(first["targets"], direct) || !reflect.DeepEqual(first["effective_targets"], effective) {
		t.Fatalf("direct = %v, effective = %v", first["targets"], first["effective_targets"])
	}
}

func BenchmarkFullInboundsWithSeveralNodes(b *testing.B) {
	repo := inboundListFixture(b, 60)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inbounds, err := repo.FullInbounds(ctx)
		if err != nil || len(inbounds) != 60 {
			b.Fatalf("inbounds = %d, error = %v", len(inbounds), err)
		}
	}
}
