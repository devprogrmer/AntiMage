package xrayconfig

import (
	"context"
	"database/sql"
	"encoding/base64"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDetachedInboundRequiresExplicitNodeAssignment(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT NOT NULL);
		INSERT INTO xray_config VALUES (1, '{"inbounds":[],"outbounds":[]}');
		CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT, xray_config_mode TEXT, xray_config TEXT);
		INSERT INTO nodes VALUES (7, 'connected', 'default', NULL), (8, 'connected', 'default', NULL);
		CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT UNIQUE, uplink BIGINT, downlink BIGINT, usage_coefficient REAL,
			upload_speed_limit BIGINT, download_speed_limit BIGINT);
		INSERT INTO inbounds VALUES (1, 'game', 13, 29, 1.5, 0, 0);
		CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT);
		INSERT INTO hosts VALUES (14, 'game');
		CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
		INSERT INTO service_hosts VALUES (3, 14);
		CREATE TABLE node_operations (id INTEGER PRIMARY KEY, operation_type TEXT, node_id INTEGER,
			user_id INTEGER, payload TEXT, status TEXT, attempts INTEGER, last_error TEXT,
			idempotency_key TEXT UNIQUE, created_at DATETIME, updated_at DATETIME);
		CREATE TABLE detached_inbounds (tag TEXT PRIMARY KEY, config TEXT NOT NULL);
		INSERT INTO detached_inbounds VALUES ('game', '{"tag":"game","protocol":"wireguard","port":51820,"settings":{}}');
	`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite", Options{})
	runtimeInbounds, err := repo.FullInbounds(ctx)
	if err != nil || len(runtimeInbounds) != 0 {
		t.Fatalf("runtime inbounds = %v, error = %v", runtimeInbounds, err)
	}
	inbounds, err := repo.ManagementInbounds(ctx)
	if err != nil || len(inbounds) != 1 {
		t.Fatalf("management inbounds = %v, error = %v", inbounds, err)
	}
	inbound := inbounds[0]
	if inbound["detached"] != true || inbound["tag"] != "game" || inbound["protocol"] != "wireguard" || inbound["uplink"] != int64(13) {
		t.Fatalf("detached inbound metadata = %v", inbound)
	}
	for _, payload := range []map[string]any{
		{"tag": "game", "protocol": "wireguard"},
		{"tag": "game", "protocol": "wireguard", "targets": []string{"master"}},
	} {
		if _, err := repo.UpdateInbound(ctx, "game", payload); err == nil {
			t.Fatal("detached inbound activated without an explicit node")
		}
	}
	result, err := repo.UpdateInbound(ctx, "game", map[string]any{
		"tag": "game", "protocol": "wireguard", "port": 51820, "targets": []string{"node:7"},
		"settings": map[string]any{
			"tunnel_port":    41940,
			"private_key":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
			"server_address": "10.64.0.1/24",
		},
	})
	if err != nil || result.Inbound["detached"] != nil || result.Inbound["uplink"] != int64(13) {
		t.Fatalf("assigned inbound = %v, error = %v", result.Inbound, err)
	}
	var remaining, matches, operations int
	if err := db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM detached_inbounds),
			(SELECT COUNT(*) FROM service_hosts sh JOIN hosts h ON h.id = sh.host_id
			 WHERE sh.service_id = 3 AND h.inbound_tag = 'game'),
			(SELECT COUNT(*) FROM node_operations WHERE node_id = 7 AND operation_type = 'sync_config')
	`).Scan(&remaining, &matches, &operations); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || matches != 1 || operations != 1 {
		t.Fatalf("drafts = %d, service mappings = %d, node syncs = %d", remaining, matches, operations)
	}
	for _, target := range []string{"master", "node:8"} {
		config, err := repo.GetTargetRawConfig(ctx, target)
		if err != nil || configHasInbound(config, "game") {
			t.Fatalf("inbound activated on %s, error = %v", target, err)
		}
	}
}
