package backup

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/xrayconfig"
	"github.com/go-sql-driver/mysql"
)

// Run against a disposable database server. Each run creates and drops its
// own pair of schemas and uses the native dump and restore clients.
func TestPortableBackupExternalMySQL(t *testing.T) {
	serverURL := os.Getenv("ANTIMAGE_TEST_PORTABLE_MYSQL")
	if serverURL == "" {
		t.Skip("set ANTIMAGE_TEST_PORTABLE_MYSQL to a disposable MySQL server URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	u, err := url.Parse(serverURL)
	if err != nil || u.User == nil || u.Host == "" {
		t.Fatal("invalid portable backup test server URL")
	}
	password, _ := u.User.Password()
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr = u.User.Username(), password, "tcp", u.Host
	cfg.ParseTime = true
	server, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	suffix := time.Now().UnixNano()
	open := func(role string, version int64) (*sql.DB, *Service) {
		t.Helper()
		name := fmt.Sprintf("antimage_migration_%s_%d", role, suffix)
		if _, err := server.ExecContext(ctx, "CREATE DATABASE "+name+" CHARACTER SET utf8mb4"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, err := sql.Open("mysql", cfg.FormatDSN())
			if err == nil {
				defer cleanup.Close()
				_, err = cleanup.Exec("DROP DATABASE " + name)
			}
			if err != nil {
				t.Error(err)
			}
		})
		options := *cfg
		options.DBName = name
		db, err := sql.Open("mysql", options.FormatDSN())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := migrations.RunMigrationsTo(ctx, db, "mysql", version); err != nil {
			t.Fatal(err)
		}
		backupURL := *u
		backupURL.Path = "/" + name
		return db, NewService(db, "mysql", backupURL.String(), WithFileRoots(nil))
	}

	source, sourceBackup := open("source", 91)
	target, targetBackup := open("target", 92)
	exec := func(db *sql.DB, stmt string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	const clientID = "11111111-1111-4111-8111-111111111111"
	wgServerBytes := [32]byte{4, 5, 6}
	wgKey := base64.StdEncoding.EncodeToString(wgServerBytes[:])
	wgClientBytes := [32]byte{1, 2, 3}
	wgClient, err := ecdh.X25519().NewPrivateKey(wgClientBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	wgClientKey := base64.StdEncoding.EncodeToString(wgClient.Bytes())
	wgClientPublicKey := base64.StdEncoding.EncodeToString(wgClient.PublicKey().Bytes())
	inbounds := []map[string]any{
		{"tag": "unlimited", "protocol": "vless", "listen": "127.0.0.1", "port": 25443,
			"settings": map[string]any{"decryption": "none", "clients": []any{}}},
		{"tag": "game", "protocol": "wireguard", "port": 51820, "settings": map[string]any{
			"private_key": wgKey, "server_address": "10.64.0.1/24", "tunnel_port": 41940,
		}},
	}
	nodeConfig, err := json.Marshal(map[string]any{"inbounds": inbounds, "outbounds": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	exec(source, `INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,xray_config)
		VALUES (7,'source-node','old.example',62050,62051,'connected','old-cert','old-key','custom',?)`, string(nodeConfig))
	exec(source, `INSERT INTO inbounds (tag,uplink,downlink) VALUES ('unlimited',13,29),('game',101,202)`)
	exec(source, `INSERT INTO services (id,name) VALUES (2,'unlimitedusers'),(3,'game')`)
	exec(source, `INSERT INTO hosts (id,inbound_tag,remark,address)
		VALUES (11,'unlimited','Unlimited','{SERVER_IP}'),(14,'game','Game','{SERVER_IP}')`)
	exec(source, `INSERT INTO service_hosts (service_id,host_id,sort) VALUES (2,11,1),(3,14,1)`)
	exec(source, `INSERT INTO users (id,username,status,used_traffic,data_limit,service_id) VALUES (20,'subscriber','active',500,1000,2)`)
	exec(source, `INSERT INTO users (id,username,status,used_traffic,data_limit,service_id) VALUES (21,'wg-subscriber','active',300,1000,3)`)
	exec(source, `INSERT INTO wireguard_devices (inbound_tag,user_id,device_index,private_key,public_key,address)
		VALUES ('game',21,1,?,?,'10.64.0.2/32')`, wgClientKey, wgClientPublicKey)
	exec(source, `INSERT INTO proxies (user_id,type,settings) VALUES (20,'vless',?)`, `{"id":"`+clientID+`"}`)
	exec(source, `INSERT INTO node_usages (created_at,node_id,uplink,downlink) VALUES (CURRENT_TIMESTAMP,7,13,29)`)
	exec(source, `INSERT INTO node_operations (operation_type,node_id,payload,status,idempotency_key)
		VALUES ('sync_config',7,'{}','pending','source-sync')`)

	archive, err := sourceBackup.Export(ctx, ScopeDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archive.Path)
	xrayBinary := os.Getenv("ANTIMAGE_TEST_XRAY_BINARY")
	var verifyOldStream func()
	if xrayBinary != "" {
		verifyOldStream = restoreTestVLESSStream(t, xrayBinary, clientID, inbounds[0])
	}
	if _, err := targetBackup.ImportForMigration(ctx, archive.Path); err != nil {
		t.Fatal(err)
	}
	if verifyOldStream != nil {
		verifyOldStream()
		t.Log("the same open VLESS connection carried traffic after database restore")
	}
	assertCount := func(query string, want int) {
		t.Helper()
		var got int
		if err := target.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("count got %d, want %d, error: %v, query: %s", got, want, err, query)
		}
	}
	assertCount(`SELECT COUNT(*) FROM nodes WHERE status <> 'deleted' OR address <> '' OR certificate IS NOT NULL OR certificate_key IS NOT NULL OR xray_config IS NOT NULL`, 0)
	assertCount(`SELECT COUNT(*) FROM node_operations`, 0)
	assertCount(`SELECT COUNT(*) FROM node_usages WHERE node_id=7 AND uplink=13 AND downlink=29`, 1)
	assertCount(`SELECT COUNT(*) FROM users WHERE id=20 AND service_id=2 AND used_traffic=500 AND data_limit=1000`, 1)
	assertCount(`SELECT COUNT(*) FROM proxies WHERE user_id=20 AND JSON_UNQUOTE(JSON_EXTRACT(settings,'$.id'))='`+clientID+`'`, 1)
	assertCount(`SELECT COUNT(*) FROM service_hosts sh JOIN hosts h ON h.id=sh.host_id
		WHERE (sh.service_id=2 AND h.inbound_tag='unlimited') OR (sh.service_id=3 AND h.inbound_tag='game')`, 2)
	assertCount(`SELECT COUNT(*) FROM detached_inbounds`, 2)
	var gotWGPrivate, gotWGPublic, gotWGAddress string
	if err := target.QueryRowContext(ctx, `SELECT private_key, public_key, address FROM wireguard_devices
		WHERE inbound_tag='game' AND user_id=21`).Scan(&gotWGPrivate, &gotWGPublic, &gotWGAddress); err != nil {
		t.Fatal(err)
	}
	if gotWGPrivate != wgClientKey || gotWGPublic != wgClientPublicKey || gotWGAddress != "10.64.0.2/32" {
		t.Fatal("WireGuard client profile changed during restore")
	}

	repo := xrayconfig.NewRepository(target, "mysql", xrayconfig.Options{})
	list, err := repo.ManagementInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	exec(target, `INSERT INTO nodes (id,name,address,port,api_port,status)
		VALUES (8,'new-node','127.0.0.1',62050,62051,'connected'),(9,'other-node','127.0.0.2',62050,62051,'connected')`)
	assigned := 0
	for _, inbound := range list {
		if inbound["detached"] != true {
			continue
		}
		tag := inbound["tag"].(string)
		inbound["targets"] = []string{"node:8"}
		if _, err := repo.UpdateInbound(ctx, tag, inbound); err != nil {
			t.Fatalf("assign %s: %v", tag, err)
		}
		assigned++
	}
	if assigned != 2 {
		t.Fatalf("assigned %d restored definitions", assigned)
	}
	assertCount(`SELECT COUNT(*) FROM detached_inbounds`, 0)
	assertCount(`SELECT COUNT(*) FROM node_operations WHERE node_id IS NULL OR node_id<>8`, 0)
	assertCount(`SELECT COUNT(*) FROM node_operations WHERE node_id=8 AND operation_type='sync_config'`, 2)
	for _, targetID := range []string{"master", "node:8", "node:9"} {
		config, err := repo.GetTargetRawConfig(ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(config["inbounds"])
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range []string{"unlimited", "game"} {
			if strings.Contains(string(raw), `"tag":"`+tag+`"`) != (targetID == "node:8") {
				t.Fatalf("%s target membership incorrect for %s", targetID, tag)
			}
		}
	}
	if xrayBinary != "" {
		config, err := repo.GetTargetRawConfig(ctx, "node:8")
		if err != nil {
			t.Fatal(err)
		}
		var restoredID string
		if err := target.QueryRowContext(ctx, `SELECT JSON_UNQUOTE(JSON_EXTRACT(settings,'$.id'))
			FROM proxies WHERE user_id = 20 AND type = 'vless'`).Scan(&restoredID); err != nil {
			t.Fatal(err)
		}
		for _, value := range config["inbounds"].([]any) {
			inbound := value.(map[string]any)
			if inbound["tag"] == "unlimited" {
				verify := restoreTestVLESSStream(t, xrayBinary, restoredID, inbound)
				verify()
				t.Log("the restored account passed VLESS traffic on the manually selected node")
			}
			if inbound["tag"] == "game" {
				settings := inbound["settings"].(map[string]any)
				if settings["private_key"] != wgKey || settings["server_address"] != "10.64.0.1/24" {
					t.Fatal("WireGuard server identity changed during restore")
				}
			}
		}
	}
	t.Log("restored native backup, preserved account/credentials/quota/service links, removed source enrollment, and assigned both protocols only to the selected node")
	if samplePath := os.Getenv("ANTIMAGE_TEST_PORTABLE_ARCHIVE"); samplePath != "" {
		sampleDB, sampleBackup := open("sample", 92)
		if _, err := sampleBackup.ImportForMigration(ctx, samplePath); err != nil {
			// A private fixture may contain credentials in native SQL errors.
			code := regexp.MustCompile(`ERROR [0-9]+(?: \([A-Z0-9]+\))?(?: at line [0-9]+)?`).FindString(err.Error())
			t.Fatalf("private archive migration failed (%T) %s", err, code)
		}
		var active, detached int
		if err := sampleDB.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM nodes WHERE status <> 'deleted' OR address <> '' OR certificate IS NOT NULL OR certificate_key IS NOT NULL),
			(SELECT COUNT(*) FROM detached_inbounds)`).Scan(&active, &detached); err != nil {
			t.Fatal("could not verify private archive migration")
		}
		if active != 0 || detached == 0 {
			t.Fatalf("private archive migration: %d active enrollments, %d detached inbounds", active, detached)
		}
		t.Logf("restored the supplied archive in isolation: zero source enrollments and %d detached inbounds", detached)
	}
}
