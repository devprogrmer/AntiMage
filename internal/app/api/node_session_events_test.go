//go:build cgo

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

func TestNodeSessionEventTracksSessionsWithoutRuntimeUserOps(t *testing.T) {
	server, db := testAdminServer(t)
	_, err := db.Exec(`
CREATE TABLE vpn_user_sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	protocol TEXT NOT NULL,
	inbound_tag TEXT NULL,
	session_id TEXT NOT NULL,
	assigned_ip TEXT NULL,
	client_ip TEXT NULL,
	device_id TEXT NULL,
	device_type TEXT NOT NULL DEFAULT 'Unknown',
	client_name TEXT NOT NULL DEFAULT 'Unknown',
	platform TEXT NOT NULL DEFAULT 'Unknown',
	started_at DATETIME NOT NULL,
	last_seen_at DATETIME NOT NULL,
	ended_at DATETIME NULL,
	UNIQUE(node_id, session_id)
)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes (id, name, status, certificate) VALUES (7, 'node-7', 'connected', 'node-cert'), (8, 'node-8', 'connected', 'other-cert')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, status, service_id, ip_limit) VALUES (42, 'pool-user', 'active', 1, 2)`); err != nil {
		t.Fatal(err)
	}

	token := nodecontroller.NodeSessionEventToken("admin-secret", 7, "node-cert")
	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"wg","inbound_tag":"wg-main","session_id":"wg:one","assigned_ip":"10.70.0.2","client_ip":"198.51.100.10","event":"start"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 1)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type = 'disable_user'`, 0)
	assertDBString(t, db, `SELECT client_ip FROM vpn_user_sessions WHERE session_id = 'wg:one'`, "198.51.100.10")

	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"ov","inbound_tag":"ov-main","session_id":"ov:two","assigned_ip":"10.66.0.2","client_ip":"198.51.100.10","event":"start"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 2)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type = 'disable_user'`, 0)

	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"l2tp","inbound_tag":"l2tp-main","session_id":"l2tp:three","assigned_ip":"10.67.0.2","event":"start"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 3)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type IN ('disable_user', 'enable_user')`, 0)

	otherToken := nodecontroller.NodeSessionEventToken("admin-secret", 8, "other-cert")
	rec := requestNodeSessionEvent(t, server, otherToken, `{"node_id":8,"user_id":42,"protocol":"wg","inbound_tag":"wg-other","session_id":"wg:other","assigned_ip":"10.70.0.4","client_ip":"198.51.100.11","event":"start"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cross-node session status = %d body=%s", rec.Code, rec.Body.String())
	}
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 3)

	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"wg","session_id":"wg:one","event":"stop"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 2)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type IN ('disable_user', 'enable_user')`, 0)

	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"l2tp","session_id":"l2tp:three","event":"stop"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 1)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type IN ('disable_user', 'enable_user')`, 0)

	postNodeSessionEvent(t, server, token, `{"node_id":7,"user_id":42,"protocol":"ov","session_id":"ov:two","event":"stop"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id = 42 AND ended_at IS NULL`, 0)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE user_id = 42 AND operation_type IN ('disable_user', 'enable_user')`, 0)
}

// TestL2TPNativePanelSessionServer supplies the real production session-event
// HTTP handler to the privileged native xl2tpd harness and verifies persisted
// start/stop state after the driver shuts it down.
func TestL2TPNativePanelSessionServer(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_API_STATE"))
	if stateDir == "" {
		t.Skip("requires native L2TP session event driver")
	}
	server, db := testAdminServer(t)
	if _, err := db.Exec(`CREATE TABLE vpn_user_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		inbound_tag TEXT NULL,
		session_id TEXT NOT NULL,
		assigned_ip TEXT NULL,
		client_ip TEXT NULL,
		device_id TEXT NULL,
		device_type TEXT NOT NULL DEFAULT 'Unknown',
		client_name TEXT NOT NULL DEFAULT 'Unknown',
		platform TEXT NOT NULL DEFAULT 'Unknown',
		started_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL,
		ended_at DATETIME NULL,
		UNIQUE(node_id, session_id)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes (id, name, status, certificate) VALUES (7, 'l2tp-native-node', 'connected', 'l2tp-native-cert')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, status, service_id, ip_limit, device_limit) VALUES (7, 'native-l2tp', 'active', 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	panelDownPath := filepath.Join(stateDir, "panel-down")
	if err := os.WriteFile(panelDownPath, []byte("offline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(panelDownPath); err == nil {
			http.Error(w, "Panel intentionally offline", http.StatusServiceUnavailable)
			return
		} else if !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		server.Handler().ServeHTTP(w, r)
	})
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	serveDoneRead := false
	go func() { serveDone <- httpServer.Serve(listener) }()
	token := nodecontroller.NodeSessionEventToken("admin-secret", 7, "l2tp-native-cert")
	ready := fmt.Sprintf("http://%s/internal/node/session-event\n%s\n", listener.Addr().String(), token)
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "api-callback.txt"), []byte(ready), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		if !serveDoneRead {
			<-serveDone
		}
	}()
	stopPath := filepath.Join(stateDir, "api-stop")
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(stopPath); err != nil {
		t.Fatalf("native driver did not finish session lifecycle: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := httpServer.Shutdown(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	serveErr := <-serveDone
	serveDoneRead = true
	if serveErr != nil && serveErr != http.ErrServerClosed {
		t.Fatal(serveErr)
	}
	var total, active, wrongIP, withDeviceID int
	var sourceIPs string
	if err := db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN ended_at IS NULL THEN 1 ELSE 0 END), SUM(CASE WHEN COALESCE(assigned_ip, '') <> '10.67.0.2' THEN 1 ELSE 0 END), SUM(CASE WHEN COALESCE(device_id, '') <> '' THEN 1 ELSE 0 END), COALESCE(GROUP_CONCAT(DISTINCT NULLIF(client_ip, '')), '') FROM vpn_user_sessions WHERE node_id = 7 AND user_id = 7 AND protocol = 'l2tp' AND inbound_tag = 'native-l2tp'`).Scan(&total, &active, &wrongIP, &withDeviceID, &sourceIPs); err != nil {
		t.Fatal(err)
	}
	if total < 2 || active != 0 || wrongIP != 0 {
		t.Fatalf("production Panel session state mismatch: sessions=%d active=%d unexpected_assigned_ip=%d", total, active, wrongIP)
	}
	t.Logf("production Panel API persisted %d L2TP sessions; all stopped, assigned IP=10.67.0.2; native source IPs=%q, device identities=%d", total, sourceIPs, withDeviceID)
}

// TestNativeKernelVPNPanelSessionServer receives production WireGuard or
// AmneziaWG session events from a privileged tunnel harness and verifies the
// panel's persisted outer/assigned addresses and stable peer identity.
func TestNativeKernelVPNPanelSessionServer(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_NATIVE_WG_SESSION_API_STATE"))
	protocol := strings.ToLower(strings.TrimSpace(os.Getenv("ANTIMAGE_NATIVE_WG_SESSION_PROTOCOL")))
	expectedAssignedIP := strings.TrimSpace(os.Getenv("ANTIMAGE_NATIVE_WG_SESSION_ASSIGNED_IP"))
	expectedClientIP := strings.TrimSpace(os.Getenv("ANTIMAGE_NATIVE_WG_SESSION_CLIENT_IP"))
	if stateDir == "" || protocol == "" {
		t.Skip("requires a privileged native kernel VPN session driver")
	}
	if protocol != "wg" && protocol != "amneziawg" {
		t.Fatalf("unsupported native kernel VPN protocol %q", protocol)
	}
	server, db := testAdminServer(t)
	if _, err := db.Exec(`CREATE TABLE vpn_user_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		inbound_tag TEXT NULL,
		session_id TEXT NOT NULL,
		assigned_ip TEXT NULL,
		client_ip TEXT NULL,
		device_id TEXT NULL,
		device_type TEXT NOT NULL DEFAULT 'Unknown',
		client_name TEXT NOT NULL DEFAULT 'Unknown',
		platform TEXT NOT NULL DEFAULT 'Unknown',
		started_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL,
		ended_at DATETIME NULL,
		UNIQUE(node_id, session_id)
	)`); err != nil {
		t.Fatal(err)
	}
	certificate := protocol + "-native-cert"
	if _, err := db.Exec(`INSERT INTO nodes (id, name, status, certificate) VALUES (7, ?, 'connected', ?)`, protocol+"-native-node", certificate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, status, service_id, ip_limit, device_limit) VALUES (7, ?, 'active', 1, 0, 0)`, "native-"+protocol); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	token := nodecontroller.NodeSessionEventToken("admin-secret", 7, certificate)
	ready := fmt.Sprintf("http://%s/internal/node/session-event\n%s\n", listener.Addr().String(), token)
	if err := os.WriteFile(filepath.Join(stateDir, "api-callback.txt"), []byte(ready), 0600); err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	serveDoneRead := false
	go func() { serveDone <- httpServer.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		if !serveDoneRead {
			<-serveDone
		}
	}()
	activePath := filepath.Join(stateDir, "api-active")
	closedPath := filepath.Join(stateDir, "api-closed")
	stopPath := filepath.Join(stateDir, "api-stop")
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		var total, active int
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN ended_at IS NULL THEN 1 ELSE 0 END), 0) FROM vpn_user_sessions WHERE node_id = 7 AND user_id = 7 AND protocol = ?`, protocol).Scan(&total, &active); err != nil {
			t.Fatal(err)
		}
		if total > 0 {
			if err := os.WriteFile(activePath, []byte("seen\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if active == 0 {
				if err := os.WriteFile(closedPath, []byte("stopped\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := os.Stat(stopPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(stopPath); err != nil {
		t.Fatalf("native driver did not finish %s session lifecycle: %v", protocol, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := httpServer.Shutdown(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := <-serveDone; err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
	serveDoneRead = true
	rows, err := db.Query(`SELECT session_id, COALESCE(assigned_ip, ''), COALESCE(client_ip, ''), COALESCE(device_id, ''), ended_at IS NULL FROM vpn_user_sessions WHERE node_id = 7 AND user_id = 7 AND protocol = ?`, protocol)
	if err != nil {
		t.Fatal(err)
	}
	var count, active int
	for rows.Next() {
		var sessionID, assignedIP, clientIP, deviceID string
		var isActive bool
		if err := rows.Scan(&sessionID, &assignedIP, &clientIP, &deviceID, &isActive); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		count++
		if isActive {
			active++
		}
		if sessionID == "" || assignedIP != expectedAssignedIP || clientIP != expectedClientIP || !strings.HasPrefix(deviceID, "wg-") {
			rows.Close()
			t.Fatalf("persisted %s session metadata mismatch: session=%q assigned=%q client=%q device=%q", protocol, sessionID, assignedIP, clientIP, deviceID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if count < 1 || active != 0 {
		t.Fatalf("production Panel %s lifecycle mismatch: sessions=%d active=%d", protocol, count, active)
	}
	t.Logf("production Panel API persisted %d native %s sessions, all stopped; real IP=%s assigned IP=%s stable peer identity recorded", count, protocol, expectedClientIP, expectedAssignedIP)
}

// TestPPTPNativePanelSessionServer runs the production session-event handler
// beside the privileged PPTP harness and checks the persisted native PPP
// start/stop events and assigned address.
func TestPPTPNativePanelSessionServer(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_PPTP_API_STATE"))
	if stateDir == "" {
		t.Skip("requires native PPTP session event driver")
	}
	server, db := testAdminServer(t)
	if _, err := db.Exec(`CREATE TABLE vpn_user_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		inbound_tag TEXT NULL,
		session_id TEXT NOT NULL,
		assigned_ip TEXT NULL,
		client_ip TEXT NULL,
		device_id TEXT NULL,
		device_type TEXT NOT NULL DEFAULT 'Unknown',
		client_name TEXT NOT NULL DEFAULT 'Unknown',
		platform TEXT NOT NULL DEFAULT 'Unknown',
		started_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL,
		ended_at DATETIME NULL,
		UNIQUE(node_id, session_id)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes (id, name, status, certificate) VALUES (7, 'pptp-native-node', 'connected', 'pptp-native-cert')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, status, service_id, ip_limit, device_limit) VALUES (7, 'native-pptp', 'active', 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	token := nodecontroller.NodeSessionEventToken("admin-secret", 7, "pptp-native-cert")
	ready := fmt.Sprintf("http://%s/internal/node/session-event\n%s\n", listener.Addr().String(), token)
	if err := os.WriteFile(filepath.Join(stateDir, "api-callback.txt"), []byte(ready), 0600); err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	serveDoneRead := false
	go func() { serveDone <- httpServer.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		if !serveDoneRead {
			<-serveDone
		}
	}()
	stopPath := filepath.Join(stateDir, "api-stop")
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(stopPath); err != nil {
		t.Fatalf("native driver did not finish PPTP session lifecycle: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := httpServer.Shutdown(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := <-serveDone; err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
	serveDoneRead = true
	rows, err := db.Query(`SELECT assigned_ip, COALESCE(client_ip, ''), COALESCE(device_id, ''), ended_at IS NULL FROM vpn_user_sessions WHERE node_id = 7 AND user_id = 7 AND protocol = 'pptp' AND inbound_tag = 'native-pptp' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var total, active, wrongIP, withClientIP, withDeviceID int
	clientIPs := []string{}
	for rows.Next() {
		var assignedIP, clientIP, deviceID string
		var stillActive bool
		if err := rows.Scan(&assignedIP, &clientIP, &deviceID, &stillActive); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		total++
		if stillActive {
			active++
		}
		if assignedIP != "10.68.0.2" {
			wrongIP++
		}
		if clientIP != "" {
			withClientIP++
			clientIPs = append(clientIPs, clientIP)
		}
		if deviceID != "" {
			withDeviceID++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if total < 1 || active != 0 || wrongIP != 0 {
		t.Fatalf("production Panel PPTP session state mismatch: sessions=%d active=%d unexpected_assigned_ip=%d", total, active, wrongIP)
	}
	t.Logf("production Panel API persisted %d native PPTP sessions, all stopped with assigned IP=10.68.0.2; source-IP values=%v (sessions=%d) and device identities=%d", total, clientIPs, withClientIP, withDeviceID)
}

func TestNodeReadyQueuesFullSync(t *testing.T) {
	server, db := testAdminServer(t)
	if _, err := db.Exec(`INSERT INTO nodes (id, name, status, certificate) VALUES (7, 'node-7', 'connected', 'node-cert')`); err != nil {
		t.Fatal(err)
	}
	token := nodecontroller.NodeSessionEventToken("admin-secret", 7, "node-cert")
	postNodeSessionEvent(t, server, token, `{"node_id":7,"event":"ready"}`)
	assertDBInt64(t, db, `SELECT COUNT(*) FROM node_operations WHERE node_id = 7 AND operation_type = 'sync_config' AND status = 'pending'`, 1)
}

func postNodeSessionEvent(t *testing.T, server *Server, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := requestNodeSessionEvent(t, server, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("session event status = %d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func requestNodeSessionEvent(t *testing.T, server *Server, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/node/session-event", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}
