package user

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestUsersListIncludesOpenTunnelSessionsInOnlineStatus(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("UTC+03:30", 3*60*60+30*60)
	defer func() { time.Local = local }()

	db, err := sql.Open("sqlite", "file:users-list-online?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, status TEXT, used_traffic BIGINT, created_at DATETIME, expire BIGINT, data_limit BIGINT, data_limit_reset_strategy TEXT, online_at DATETIME, service_id BIGINT, admin_id BIGINT, credential_key TEXT, subadress TEXT, flow TEXT, on_hold_expire_duration BIGINT)`,
		`CREATE TABLE user_presence (user_id INTEGER PRIMARY KEY, online_at DATETIME NOT NULL)`,
		`CREATE TABLE admins (id INTEGER PRIMARY KEY, username TEXT)`,
		`CREATE TABLE services (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE user_usage_logs (user_id BIGINT, used_traffic_at_reset BIGINT)`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT)`,
		`CREATE TABLE user_online_ips (node_id BIGINT, user_id BIGINT, last_seen_at DATETIME)`,
		`CREATE TABLE vpn_user_sessions (node_id BIGINT, user_id BIGINT, last_seen_at DATETIME, ended_at DATETIME)`,
		`INSERT INTO nodes (id, status) VALUES (1, 'connected')`,
		`INSERT INTO users (id, username, status, used_traffic, created_at, online_at) VALUES (1, 'tunnel-user', 'active', 0, CURRENT_TIMESTAMP, NULL), (2, 'stale-user', 'active', 0, CURRENT_TIMESTAMP, datetime('now', '-10 minutes'))`,
		`INSERT INTO vpn_user_sessions (node_id, user_id, last_seen_at, ended_at) VALUES (1, 1, CURRENT_TIMESTAMP, NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	repo := NewRepository(db, "sqlite")
	onlineAt := func(rows []usersListRow, username string) *string {
		for _, row := range rows {
			if row.item.Username == username {
				return row.item.OnlineAt
			}
		}
		t.Fatalf("missing user %q in %#v", username, rows)
		return nil
	}
	isOnline := func(rows []usersListRow, username string) bool {
		for _, row := range rows {
			if row.item.Username == username {
				return row.item.IsOnline
			}
		}
		t.Fatalf("missing user %q in %#v", username, rows)
		return false
	}
	rows, err := repo.usersRows(context.Background(), usersFilter{}, UsersListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || onlineAt(rows, "tunnel-user") == nil {
		t.Fatalf("expected open tunnel session to be online, got %#v", rows)
	}
	if !isOnline(rows, "tunnel-user") || isOnline(rows, "stale-user") {
		t.Fatalf("unexpected explicit online flags: %#v", rows)
	}
	summary, err := repo.usersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.onlineTotal != 1 {
		t.Fatalf("expected one online user, got %d", summary.onlineTotal)
	}
	if summary.total != 2 || summary.statusBreakdown["active"] != 2 || summary.usageTotal != 0 {
		t.Fatalf("unexpected combined summary: %#v", summary)
	}
	if _, err := db.Exec(`UPDATE nodes SET status = 'deleted' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	cached, err := repo.usersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if cached.onlineTotal != 1 {
		t.Fatalf("expected the repeated summary to use its short cache, got %d", cached.onlineTotal)
	}
	summary, err = repo.queryUsersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.onlineTotal != 0 {
		t.Fatalf("expected deleted node sessions to be offline, got %d", summary.onlineTotal)
	}
	if _, err := db.Exec(`UPDATE nodes SET status = 'connected' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`UPDATE vpn_user_sessions SET ended_at = ?`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rows, err = repo.usersRows(context.Background(), usersFilter{}, UsersListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if value := onlineAt(rows, "tunnel-user"); value != nil {
		t.Fatalf("expected ended tunnel session to be offline, got %q", *value)
	}
	summary, err = repo.queryUsersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.onlineTotal != 0 {
		t.Fatalf("expected stale online_at to be ignored, got %d", summary.onlineTotal)
	}
	if _, err := db.Exec(`INSERT INTO user_online_ips (node_id, user_id, last_seen_at) VALUES (1, 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	rows, err = repo.usersRows(context.Background(), usersFilter{}, UsersListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	value := onlineAt(rows, "tunnel-user")
	if value == nil {
		t.Fatal("expected an online Xray user to have a last connection")
	}
	seenAt, ok := parseDBTime(*value)
	if !ok || time.Since(seenAt) > 5*time.Second {
		t.Fatalf("expected an online Xray user to have a current last connection, got %v", value)
	}
	summary, err = repo.queryUsersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.onlineTotal != 1 {
		t.Fatalf("expected fresh Xray activity to be online, got %d", summary.onlineTotal)
	}
	if _, err := db.Exec(`UPDATE users SET online_at = CURRENT_TIMESTAMP WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	summary, err = repo.queryUsersSummary(context.Background(), usersFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.onlineTotal != 2 {
		t.Fatalf("expected the connection marker to count without a usable client IP, got %d", summary.onlineTotal)
	}
	onlines, err := repo.OnlineUsernames(context.Background(), UsersListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlines) != 2 || onlines[0] != "stale-user" || onlines[1] != "tunnel-user" {
		t.Fatalf("online usernames = %#v", onlines)
	}
}

func TestUsersListKeepsRowsWhenConfigLinkGenerationFails(t *testing.T) {
	db, err := sql.Open("sqlite", "file:users-list-link-error?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, status TEXT, used_traffic BIGINT, created_at DATETIME, expire BIGINT, data_limit BIGINT, data_limit_reset_strategy TEXT, online_at DATETIME, service_id BIGINT, admin_id BIGINT, credential_key TEXT, subadress TEXT, flow TEXT, on_hold_expire_duration BIGINT)`,
		`CREATE TABLE admins (id INTEGER PRIMARY KEY, username TEXT, subscription_domain TEXT, subscription_settings TEXT)`,
		`CREATE TABLE services (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE user_usage_logs (user_id BIGINT, used_traffic_at_reset BIGINT)`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT, xray_config_mode TEXT, xray_config TEXT, address TEXT)`,
		`CREATE TABLE user_presence (user_id INTEGER PRIMARY KEY, online_at DATETIME NOT NULL)`,
		`CREATE TABLE user_online_ips (node_id BIGINT, user_id BIGINT, protocol TEXT, ip TEXT, last_seen_at DATETIME)`,
		`CREATE TABLE vpn_user_sessions (node_id BIGINT, user_id BIGINT, last_seen_at DATETIME, ended_at DATETIME)`,
		`CREATE TABLE panel_settings (id INTEGER PRIMARY KEY, default_subscription_type TEXT)`,
		`CREATE TABLE subscription_settings (id INTEGER PRIMARY KEY, subscription_url_prefix TEXT, subscription_path TEXT, subscription_ports TEXT)`,
		`CREATE TABLE jwt (id INTEGER PRIMARY KEY, subscription_secret_key TEXT, vmess_mask TEXT, vless_mask TEXT)`,
		`CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT)`,
		`CREATE TABLE haproxy_configs (id INTEGER PRIMARY KEY, enabled INTEGER)`,
		`CREATE TABLE haproxy_targets (id INTEGER PRIMARY KEY, config_id INTEGER, listeners TEXT)`,
		`CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, remark TEXT, address TEXT, dns_primary TEXT, dns_secondary TEXT, address_options TEXT, address_selection_mode TEXT, address_ttl_seconds INTEGER, port INTEGER, path TEXT, sni TEXT, sni_options TEXT, sni_selection_mode TEXT, sni_ttl_seconds INTEGER, host TEXT, host_options TEXT, host_selection_mode TEXT, host_ttl_seconds INTEGER, security TEXT, alpn TEXT, fingerprint TEXT, verify_peer_cert_by_name TEXT, pinned_peer_cert_sha256 TEXT, allowinsecure INTEGER, is_disabled INTEGER, mux_enable INTEGER, fragment_setting TEXT, noise_setting TEXT, finalmask TEXT, random_user_agent INTEGER, use_sni_as_host INTEGER)`,
		`CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER, sort INTEGER)`,
		`CREATE TABLE proxies (id INTEGER PRIMARY KEY, user_id INTEGER, type TEXT, settings TEXT)`,
		`INSERT INTO admins (id, username) VALUES (1, 'owner')`,
		`INSERT INTO services (id, name) VALUES (1, 'svc')`,
		`INSERT INTO panel_settings (id, default_subscription_type) VALUES (1, 'key')`,
		`INSERT INTO subscription_settings (id, subscription_url_prefix, subscription_path, subscription_ports) VALUES (1, '', 'sub', '')`,
		`INSERT INTO jwt (id, subscription_secret_key) VALUES (1, 'sub-secret')`,
		`INSERT INTO xray_config (id, data) VALUES (1, '{"inbounds":[{"tag":"bad-vless","protocol":"vless","port":443,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"none"}}],"outbounds":[]}')`,
		`INSERT INTO hosts (id, inbound_tag, remark, address, dns_primary, dns_secondary, address_selection_mode, sni_selection_mode, host_selection_mode, security, alpn, fingerprint, allowinsecure, is_disabled, mux_enable, random_user_agent, use_sni_as_host) VALUES (1, 'bad-vless', 'bad', 'example.test', '', '', 'random', 'random', 'random', 'inbound_default', 'none', 'none', 0, 0, 0, 0, 0)`,
		`INSERT INTO service_hosts (service_id, host_id, sort) VALUES (1, 1, 0)`,
		`INSERT INTO users (id, username, status, used_traffic, created_at, service_id, admin_id, credential_key) VALUES (1, 'visible-user', 'active', 0, CURRENT_TIMESTAMP, 1, 1, '')`,
		`INSERT INTO proxies (id, user_id, type, settings) VALUES (1, 1, 'vless', '{}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	repo := NewRepository(db, "sqlite")
	limit := int64(10)
	result, err := repo.UsersList(context.Background(), UsersListRequest{
		IncludeLinks: true,
		Limit:        &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Users) != 1 {
		t.Fatalf("expected one listed user, got %#v", result)
	}
	if result.Users[0].Username != "visible-user" {
		t.Fatalf("unexpected user: %#v", result.Users[0])
	}
	if result.Users[0].LinkError == "" || !strings.Contains(result.Users[0].LinkError, "UUID is required") {
		t.Fatalf("expected link error to be preserved, got %#v", result.Users[0])
	}
}
