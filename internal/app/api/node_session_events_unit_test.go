package api

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNodeSessionAdmissionClosesAddresslessLegacySession(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE users (id INTEGER PRIMARY KEY, ip_limit INTEGER NOT NULL, device_limit INTEGER NOT NULL DEFAULT 0);
CREATE TABLE vpn_user_sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	protocol TEXT NOT NULL,
	inbound_tag TEXT,
	session_id TEXT NOT NULL,
	assigned_ip TEXT,
	client_ip TEXT,
	device_id TEXT,
	device_type TEXT NOT NULL DEFAULT 'Unknown',
	client_name TEXT NOT NULL DEFAULT 'Unknown',
	platform TEXT NOT NULL DEFAULT 'Unknown',
	started_at DATETIME NOT NULL,
	last_seen_at DATETIME NOT NULL,
	ended_at DATETIME,
	UNIQUE(node_id, session_id)
);
INSERT INTO users (id, ip_limit) VALUES (42, 1);
INSERT INTO vpn_user_sessions (node_id, user_id, protocol, session_id, started_at, last_seen_at)
VALUES (7, 42, 'ov', 'ov:legacy', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}

	server := &Server{db: db}
	err = server.applyNodeSessionEvent(context.Background(), nodeSessionEventPayload{
		NodeID:     7,
		UserID:     42,
		Protocol:   "ov",
		InboundTag: "ov-main",
		SessionID:  "ov:new",
		AssignedIP: "10.66.0.2",
		ClientIP:   "198.51.100.10",
		Event:      "start",
	})
	if err != nil {
		t.Fatal(err)
	}
	var active, closed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpn_user_sessions WHERE session_id = 'ov:legacy' AND ended_at IS NOT NULL`).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if active != 1 || closed != 1 {
		t.Fatalf("active=%d closed_legacy=%d", active, closed)
	}
}

func TestNodeSessionAdmissionReplacesOpenVPNReconnect(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE users (id INTEGER PRIMARY KEY, ip_limit INTEGER NOT NULL, device_limit INTEGER NOT NULL DEFAULT 0);
CREATE TABLE vpn_user_sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	protocol TEXT NOT NULL,
	inbound_tag TEXT,
	session_id TEXT NOT NULL,
	assigned_ip TEXT,
	client_ip TEXT,
	device_id TEXT,
	device_type TEXT NOT NULL DEFAULT 'Unknown',
	client_name TEXT NOT NULL DEFAULT 'Unknown',
	platform TEXT NOT NULL DEFAULT 'Unknown',
	started_at DATETIME NOT NULL,
	last_seen_at DATETIME NOT NULL,
	ended_at DATETIME,
	UNIQUE(node_id, session_id)
);
INSERT INTO users (id, ip_limit) VALUES (42, 1);
INSERT INTO vpn_user_sessions (node_id, user_id, protocol, inbound_tag, session_id, assigned_ip, client_ip, started_at, last_seen_at)
VALUES (7, 42, 'ov', 'ov-main', 'ov:first', '10.66.0.2', '198.51.100.10', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);`); err != nil {
		t.Fatal(err)
	}

	server := &Server{db: db}
	err = server.applyNodeSessionEvent(context.Background(), nodeSessionEventPayload{
		NodeID:     7,
		UserID:     42,
		Protocol:   "ov",
		InboundTag: "ov-main",
		SessionID:  "ov:second",
		AssignedIP: "10.66.0.2",
		ClientIP:   "198.51.100.11",
		Event:      "start",
	})
	if err != nil {
		t.Fatal(err)
	}
	var active, replaced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpn_user_sessions WHERE session_id = 'ov:first' AND ended_at IS NOT NULL`).Scan(&replaced); err != nil {
		t.Fatal(err)
	}
	if active != 1 || replaced != 1 {
		t.Fatalf("active=%d replaced=%d", active, replaced)
	}
}

func TestNodeSessionAdmissionUsesStableDeviceIdentityNotIP(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "device-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY,ip_limit INTEGER NOT NULL,device_limit INTEGER NOT NULL); CREATE TABLE vpn_user_sessions(id INTEGER PRIMARY KEY AUTOINCREMENT,node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,protocol TEXT NOT NULL,inbound_tag TEXT,session_id TEXT NOT NULL,assigned_ip TEXT,client_ip TEXT,device_id TEXT,device_type TEXT NOT NULL DEFAULT 'Unknown',client_name TEXT NOT NULL DEFAULT 'Unknown',platform TEXT NOT NULL DEFAULT 'Unknown',started_at DATETIME NOT NULL,last_seen_at DATETIME NOT NULL,ended_at DATETIME,UNIQUE(node_id,session_id)); INSERT INTO users(id,ip_limit,device_limit) VALUES(42,0,1);`); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: db}
	first := nodeSessionEventPayload{NodeID: 7, UserID: 42, Protocol: "wg", SessionID: "wg:first", ClientIP: "198.51.100.10", DeviceID: "wg-a", Event: "start"}
	if err := server.applyNodeSessionEvent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	first.SessionID = "wg:reconnect"
	first.ClientIP = "198.51.100.11"
	if err := server.applyNodeSessionEvent(context.Background(), first); err != nil {
		t.Fatalf("same device rejected after IP change: %v", err)
	}
	second := nodeSessionEventPayload{NodeID: 7, UserID: 42, Protocol: "wg", SessionID: "wg:second", DeviceID: "wg-b", Event: "start"}
	if err := server.applyNodeSessionEvent(context.Background(), second); !errors.Is(err, errDeviceLimitReached) {
		t.Fatalf("error=%v want device limit", err)
	}
	second.Protocol = "pptp"
	second.SessionID = "pptp:unknown"
	second.DeviceID = ""
	if err := server.applyNodeSessionEvent(context.Background(), second); err != nil {
		t.Fatalf("unknown native device received fake enforcement: %v", err)
	}
}
