//go:build linux && cgo && phasec_e2e

// This suite is the real runtime gate for the Phase C roadmap evidence table.
package api

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/nodeclient"
	"github.com/antimage/antimage/internal/app/xrayconfig"
	platformdb "github.com/antimage/antimage/internal/platform/db"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	_ "modernc.org/sqlite"
)

var phaseCWireGuardInbound = fmt.Sprintf("phase-c-wg-%x", time.Now().UnixNano())

func TestPhaseCWireGuardE2E(t *testing.T) {
	if os.Getenv("ANTIMAGE_PHASE_C_WG_E2E") != "1" {
		t.Skip("set ANTIMAGE_PHASE_C_WG_E2E=1 to run the real Panel/Node/WireGuard acceptance test")
	}
	if os.Geteuid() != 0 {
		t.Fatal("real WireGuard acceptance test must run as root so the production Node can create and inspect its isolated test interface")
	}
	panelBin := requireE2EBinary(t, "ANTIMAGE_E2E_PANEL_BIN")
	nodeBin := requireE2EBinary(t, "ANTIMAGE_E2E_NODE_BIN")
	for _, tool := range []string{"ip", "wg", "sysctl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required WireGuard E2E tool %q is unavailable: %v", tool, err)
		}
	}

	root := t.TempDir()
	databasePath := filepath.Join(root, "phase-c.sqlite")
	databaseURL := "sqlite:///" + filepath.ToSlash(databasePath)
	pool, err := platformdb.Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.DB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := migrations.RunMigrations(ctx, pool.DB, "sqlite"); err != nil {
		t.Fatalf("migrate temporary E2E database: %v", err)
	}

	certPEM, keyPEM := phaseCE2ECertificate(t)
	certDir := filepath.Join(root, "node-certificate")
	dataDir := filepath.Join(root, "node-data")
	appDir := filepath.Join(root, "node-app")
	for _, dir := range []string{certDir, dataDir, appDir, filepath.Join(root, "certificates"), filepath.Join(root, "external-apps")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	certPath, keyPath := filepath.Join(certDir, "node.crt"), filepath.Join(certDir, "node.key")
	if err := os.WriteFile(certPath, []byte(certPEM), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}

	servicePort := phaseCFreePort(t, "tcp")
	panelPort := phaseCFreePort(t, "tcp")
	wgPort := phaseCFreePort(t, "udp")
	interfaceName := phaseCWireGuardInterface(phaseCWireGuardInbound)
	userspaceWG := false
	probeName := "p" + interfaceName[1:]
	probeOutput, probeErr := phaseCCommand("ip", "link", "add", "dev", probeName, "type", "wireguard")
	if probeErr == nil {
		_, _ = phaseCCommand("ip", "link", "delete", "dev", probeName)
	}
	if probeErr != nil || os.Getenv("ANTIMAGE_E2E_WIREGUARD_BACKEND") == "userspace" {
		wireGuardGo := strings.TrimSpace(os.Getenv("ANTIMAGE_E2E_WIREGUARD_GO_BIN"))
		if wireGuardGo == "" {
			wireGuardGo, probeErr = exec.LookPath("wireguard-go")
		}
		if probeErr != nil || wireGuardGo == "" {
			t.Fatalf("kernel WireGuard interface creation failed (%s), and wireguard-go is unavailable: %v", strings.TrimSpace(string(probeOutput)), probeErr)
		}
		if _, err := os.Stat("/dev/net/tun"); err != nil {
			t.Fatalf("kernel WireGuard failed and userspace /dev/net/tun is unavailable: %v", err)
		}
		userspaceWG = true
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPrivateKey := base64.StdEncoding.EncodeToString(private.Bytes())
	seedPhaseCWireGuardDB(t, ctx, pool.DB, certPEM, keyPEM, servicePort, wgPort, serverPrivateKey, interfaceName, userspaceWG, dataDir)

	panelLog := filepath.Join(root, "panel.log")
	nodeLog := filepath.Join(root, "node.log")
	t.Cleanup(func() {
		_, _ = phaseCCommand("ip", "link", "delete", "dev", interfaceName)
	})
	if userspaceWG {
		wireGuardGo := strings.TrimSpace(os.Getenv("ANTIMAGE_E2E_WIREGUARD_GO_BIN"))
		if wireGuardGo == "" {
			wireGuardGo, _ = exec.LookPath("wireguard-go")
		}
		wgProcess, err := phaseCStartCommand(wireGuardGo, []string{"-f", interfaceName}, root, filepath.Join(root, "wireguard-go.log"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { phaseCStopProcess(t, wgProcess) })
		phaseCWait(t, 10*time.Second, "wireguard-go TUN interface", func() bool {
			_, err := phaseCCommand("ip", "link", "show", "dev", interfaceName)
			return err == nil
		})
		if output, err := phaseCCommand("ip", "link", "set", "dev", interfaceName, "alias", "antimage:"+phaseCWireGuardInbound); err != nil {
			t.Fatalf("mark userspace interface ownership: %s (%v)", strings.TrimSpace(string(output)), err)
		}
	}
	node, err := phaseCStartProcess(nodeBin, root, nodeLog, map[string]string{
		"ANTIMAGE_NODE_NAME":           "phase-c-node",
		"SERVICE_HOST":                 "127.0.0.1",
		"SERVICE_PORT":                 fmt.Sprint(servicePort),
		"ANTIMAGE_NODE_DATA_DIR":       dataDir,
		"ANTIMAGE_NODE_APP_DIR":        appDir,
		"ANTIMAGE_NODE_INSTALL_MODE":   "source",
		"SSL_CERT_FILE":                certPath,
		"SSL_KEY_FILE":                 keyPath,
		"XRAY_API_PORT":                "10085",
		"ANTIMAGE_NODE_UPDATE_CHANNEL": "stable",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phaseCStopProcess(t, node) })

	panel, err := phaseCStartProcess(panelBin, root, panelLog, map[string]string{
		"SQLALCHEMY_DATABASE_URL":                databaseURL,
		"DATABASE_URL":                           databaseURL,
		"ANTIMAGE_CERT_BASE":                     filepath.Join(root, "certificates"),
		"ANTIMAGE_EXTERNAL_APPS_BASE":            filepath.Join(root, "external-apps"),
		"ANTIMAGE_GATEWAY_ADDR":                  fmt.Sprintf("127.0.0.1:%d", panelPort),
		"ANTIMAGE_ENV_FILE":                      filepath.Join(root, "empty.env"),
		"ANTIMAGE_NODE_OPERATIONS_POLL_INTERVAL": "200ms",
		"ANTIMAGE_USER_LIFECYCLE_INTERVAL":       "off",
		"ANTIMAGE_ADMIN_LIFECYCLE_INTERVAL":      "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phaseCStopProcess(t, panel) })
	t.Cleanup(func() {
		if t.Failed() {
			phaseCPrintFailureEvidence(t, pool.DB, panelLog, nodeLog, interfaceName, servicePort, certPEM, keyPEM)
		}
	})

	client := &http.Client{Timeout: 8 * time.Second}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", panelPort)
	phaseCWait(t, 35*time.Second, "real Panel API health", func() bool {
		resp, err := client.Get(baseURL + "/__antimage_go/api_healthz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	token := phaseCLogin(t, client, baseURL)

	// Recovery of this stale connecting node uses the production controller's
	// TLS/gRPC Hello -> Connect -> fenced full-sync path.
	phaseCWait(t, 75*time.Second, "Node TLS/gRPC registration and all real WireGuard peers", func() bool {
		var status string
		if err := pool.DB.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id=42`).Scan(&status); err != nil || status != "connected" {
			return false
		}
		peers, err := phaseCWireGuardPeers(interfaceName)
		return err == nil && len(peers) == 3
	})
	var capabilityJSON string
	if err := pool.DB.QueryRowContext(ctx, `SELECT node_capabilities FROM nodes WHERE id=42`).Scan(&capabilityJSON); err != nil {
		t.Fatalf("read connected Node runtime capabilities: %v", err)
	}
	var capabilities []string
	if err := json.Unmarshal([]byte(capabilityJSON), &capabilities); err != nil {
		t.Fatalf("decode connected Node runtime capabilities %q: %v", capabilityJSON, err)
	}
	if !phaseCContains(capabilities, "shared_fencing_v1") || !phaseCContains(capabilities, "command_idempotency_v1") {
		t.Fatalf("real Node handshake did not persist required runtime capabilities: %v", capabilities)
	}
	phaseCRevokeConnectedPeer(t, ctx, pool.DB, client, baseURL, token, interfaceName)

	var targetPublic, controlPublic, targetDeviceID string
	if err := pool.DB.QueryRowContext(ctx, `SELECT public_key FROM wireguard_devices WHERE inbound_tag=? AND user_id=1001 AND device_index=0`, phaseCWireGuardInbound).Scan(&targetPublic); err != nil {
		t.Fatal(err)
	}
	if err := pool.DB.QueryRowContext(ctx, `SELECT public_key FROM wireguard_devices WHERE inbound_tag=? AND user_id=1002 AND device_index=0`, phaseCWireGuardInbound).Scan(&controlPublic); err != nil {
		t.Fatal(err)
	}
	targetDeviceID = safeDeviceID(targetPublic)
	initialPeers, err := phaseCWireGuardPeers(interfaceName)
	if err != nil || !initialPeers[targetPublic] || !initialPeers[controlPublic] || len(initialPeers) != 2 {
		t.Fatalf("pre-revoke live runtime lacks target/control peers: peers=%v err=%v", initialPeers, err)
	}
	if _, err := pool.DB.ExecContext(ctx, `INSERT INTO vpn_user_sessions (node_id,user_id,protocol,inbound_tag,session_id,device_id,assigned_ip,started_at,last_seen_at) VALUES (42,1001,'wg',?,'target-session',?,'10.230.60.2',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),(42,1002,'wg',?,'control-session',?,'10.230.60.3',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, phaseCWireGuardInbound, targetDeviceID, phaseCWireGuardInbound, safeDeviceID(controlPublic)); err != nil {
		t.Fatal(err)
	}
	var beforeID int64
	if err := pool.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM node_operations`).Scan(&beforeID); err != nil {
		t.Fatal(err)
	}
	// Keep the runtime witness disconnected for the revoke request. Delivery
	// must remain durable and retryable until this same real Node reconnects.
	// A crashed agent leaves the live tunnel in place. Reconnect must update
	// that runtime, so a restart cannot hide a failed revoke by clearing peers.
	phaseCStopWithSignal(t, node, syscall.SIGKILL)
	node = nil
	phaseCWait(t, 5*time.Second, "Node gRPC listener shutdown", func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", servicePort), 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return false
		}
		return true
	})
	phaseCWait(t, 70*time.Second, "Panel records the Node as disconnected", func() bool {
		var status string
		return pool.DB.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id=42`).Scan(&status) == nil && status == "error"
	})
	offlinePeers, err := phaseCWireGuardPeers(interfaceName)
	if err != nil || !offlinePeers[targetPublic] || !offlinePeers[controlPublic] {
		t.Fatalf("agent crash did not preserve the runtime witness: err=%v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/api/user/phase-c-target/devices?protocol=wg&inbound_tag="+url.QueryEscape(phaseCWireGuardInbound)+"&device_id="+url.QueryEscape(targetDeviceID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("real authenticated revoke API request failed: %v", err)
	}
	responseBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("real revoke API status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	var ended sql.NullString
	if err := pool.DB.QueryRowContext(ctx, `SELECT ended_at FROM vpn_user_sessions WHERE node_id=42 AND user_id=1001 AND device_id=?`, targetDeviceID).Scan(&ended); err != nil || !ended.Valid {
		t.Fatalf("target session was not ended in revoke transaction: ended=%v err=%v", ended, err)
	}
	var oldDeviceRows int
	if err := pool.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wireguard_devices WHERE inbound_tag=? AND user_id=1001 AND public_key=?`, phaseCWireGuardInbound, targetPublic).Scan(&oldDeviceRows); err != nil || oldDeviceRows != 0 {
		t.Fatalf("revoked device row remains: rows=%d err=%v", oldDeviceRows, err)
	}
	var tombstoneRows int
	if err := pool.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wireguard_device_revocations WHERE inbound_tag=? AND user_id=1001 AND device_index=0 AND public_key=?`, phaseCWireGuardInbound, targetPublic).Scan(&tombstoneRows); err != nil || tombstoneRows != 1 {
		t.Fatalf("revoked device identity was not durably fenced from re-provisioning: rows=%d err=%v", tombstoneRows, err)
	}
	var revokeOperationID int64
	if err := pool.DB.QueryRowContext(ctx, `SELECT id FROM node_operations WHERE operation_type='sync_config' AND node_id IS NULL AND user_id=1001 AND id>? AND payload LIKE '%device_revoke%' ORDER BY id DESC LIMIT 1`, beforeID).Scan(&revokeOperationID); err != nil {
		t.Fatalf("transactional global sync operation is missing: %v", err)
	}
	var nodeSyncOperationID int64
	var disconnectedStatus string
	phaseCWait(t, 20*time.Second, "durable node-specific revoke sync while disconnected", func() bool {
		err := pool.DB.QueryRowContext(ctx, `SELECT id,status FROM node_operations WHERE operation_type='sync_config' AND node_id=42 AND id>? AND payload LIKE '%device_revoke%' ORDER BY id DESC LIMIT 1`, beforeID).Scan(&nodeSyncOperationID, &disconnectedStatus)
		return err == nil && disconnectedStatus != "done" && disconnectedStatus != "failed"
	})
	node, err = phaseCStartProcess(nodeBin, root, nodeLog, map[string]string{
		"ANTIMAGE_NODE_NAME":           "phase-c-node",
		"SERVICE_HOST":                 "127.0.0.1",
		"SERVICE_PORT":                 fmt.Sprint(servicePort),
		"ANTIMAGE_NODE_DATA_DIR":       dataDir,
		"ANTIMAGE_NODE_APP_DIR":        appDir,
		"ANTIMAGE_NODE_INSTALL_MODE":   "source",
		"SSL_CERT_FILE":                certPath,
		"SSL_KEY_FILE":                 keyPath,
		"XRAY_API_PORT":                "10085",
		"ANTIMAGE_NODE_UPDATE_CHANNEL": "stable",
	})
	if err != nil {
		t.Fatal(err)
	}

	manualRecoveryPhase := ""
	phaseCWait(t, 120*time.Second, "reconnected Node sync ACK, target peer removal and control peer preservation", func() bool {
		var globalStatus string
		var nodeStatus string
		globalErr := pool.DB.QueryRowContext(ctx, `SELECT status FROM node_operations WHERE id=?`, revokeOperationID).Scan(&globalStatus)
		nodeErr := pool.DB.QueryRowContext(ctx, `SELECT status FROM node_operations WHERE id=?`, nodeSyncOperationID).Scan(&nodeStatus)
		if globalErr == nil && nodeErr == nil && globalStatus == "done" && nodeStatus == "done" {
			peers, err := phaseCWireGuardPeers(interfaceName)
			return err == nil && !peers[targetPublic] && peers[controlPublic] && len(peers) == 1
		}
		_ = pool.DB.QueryRowContext(ctx, `SELECT phase FROM operations WHERE id=?`, fmt.Sprintf("sync_config-%d", nodeSyncOperationID)).Scan(&manualRecoveryPhase)
		return manualRecoveryPhase == "manual_recovery_required"
	})
	if manualRecoveryPhase == "manual_recovery_required" {
		t.Fatalf("Node sync entered %s instead of completing after reconnect", manualRecoveryPhase)
	}
	var persistedOperationType, persistedOperationStatus string
	if err := pool.DB.QueryRowContext(ctx, `SELECT operation_type,status FROM node_operations WHERE id=?`, nodeSyncOperationID).Scan(&persistedOperationType, &persistedOperationStatus); err != nil || persistedOperationType != "sync_config" || persistedOperationStatus != "done" {
		t.Fatalf("node ACK result was not persisted: type=%q status=%q err=%v", persistedOperationType, persistedOperationStatus, err)
	}
	var controlSessionEnded int
	if err := pool.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM vpn_user_sessions WHERE node_id=42 AND user_id=1002 AND ended_at IS NOT NULL`).Scan(&controlSessionEnded); err != nil || controlSessionEnded != 0 {
		t.Fatalf("unrelated control session changed: ended=%d err=%v", controlSessionEnded, err)
	}

	// Retrying the same request cannot enqueue another action after the first
	// transaction removed the stable device identity.
	req, _ = http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/api/user/phase-c-target/devices?protocol=wg&inbound_tag="+url.QueryEscape(phaseCWireGuardInbound)+"&device_id="+url.QueryEscape(targetDeviceID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("repeated revoke request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("repeated revoke status=%d, want 404 for already removed identity", resp.StatusCode)
	}
	if err := pool.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_operations WHERE operation_type='sync_config' AND node_id IS NULL AND user_id=1001 AND id>? AND payload LIKE '%device_revoke%'`, beforeID).Scan(&oldDeviceRows); err != nil || oldDeviceRows != 1 {
		t.Fatalf("repeated revoke enqueued duplicate work: count=%d err=%v", oldDeviceRows, err)
	}
	t.Log("crashed Node reconnect: durable revoke sync removed the target peer, preserved the control peer/session, and persisted the correlated successful ACK; repeat revoke returned 404 without duplicate work")
}

func phaseCRevokeConnectedPeer(t *testing.T, ctx context.Context, db *sql.DB, client *http.Client, baseURL, token, iface string) {
	t.Helper()
	var targetPublic string
	if err := db.QueryRowContext(ctx, `SELECT public_key FROM wireguard_devices WHERE inbound_tag=? AND user_id=1003`, phaseCWireGuardInbound).Scan(&targetPublic); err != nil {
		t.Fatal(err)
	}
	beforePeers, err := phaseCWireGuardPeers(iface)
	if err != nil || len(beforePeers) != 3 || !beforePeers[targetPublic] {
		t.Fatalf("connected revoke precondition failed: peer count=%d err=%v", len(beforePeers), err)
	}
	beforeInterface, err := net.InterfaceByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := safeDeviceID(targetPublic)
	if _, err := db.ExecContext(ctx, `INSERT INTO vpn_user_sessions (node_id,user_id,protocol,inbound_tag,session_id,device_id,started_at,last_seen_at) VALUES (42,1003,'wg',?,'online-target-session',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, phaseCWireGuardInbound, deviceID); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/api/user/phase-c-online-target/devices?protocol=wg&inbound_tag="+url.QueryEscape(phaseCWireGuardInbound)+"&device_id="+url.QueryEscape(deviceID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connected revoke API status=%d", resp.StatusCode)
	}
	phaseCWait(t, 30*time.Second, "connected revoke delivery, live selective peer removal and persisted ACK", func() bool {
		var ackCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_operations WHERE node_id=42 AND operation_type='sync_config' AND status='done' AND payload LIKE '%device_revoke%'`).Scan(&ackCount); err != nil || ackCount != 1 {
			return false
		}
		peers, err := phaseCWireGuardPeers(iface)
		if err != nil || len(peers) != 2 || peers[targetPublic] {
			return false
		}
		for peer := range beforePeers {
			if peer != targetPublic && !peers[peer] {
				return false
			}
		}
		return true
	})
	afterInterface, err := net.InterfaceByName(iface)
	if err != nil || afterInterface.Index != beforeInterface.Index {
		t.Fatalf("connected revoke reset the interface instead of selectively removing the peer: err=%v", err)
	}
	var ended int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vpn_user_sessions WHERE user_id=1003 AND ended_at IS NOT NULL`).Scan(&ended); err != nil || ended != 1 {
		t.Fatalf("connected revoke session closure missing: ended=%d err=%v", ended, err)
	}
	t.Log("connected TLS/gRPC Node: authenticated revoke removed only the target peer, preserved the interface and two control peers, and persisted sync ACK")
}

func seedPhaseCWireGuardDB(t *testing.T, ctx context.Context, db *sql.DB, cert, key string, servicePort, wgPort int, privateKey, interfaceName string, explicitInterface bool, dataDir string) {
	t.Helper()
	passwordHash, err := adminapp.HashPassword("phase-c-e2e-local-password")
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admins (id,username,hashed_password,role,permissions,status) VALUES (1,'phase-c-owner',?,?,?,'active')`, passwordHash, string(adminapp.RoleFullAccess), string(permissions)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO services (id,name) VALUES (1,'phase-c-service')`,
		`INSERT INTO service_hosts (service_id,host_id,sort) VALUES (1,1,0)`,
		`INSERT INTO users (id,username,status,admin_id,service_id,device_limit,credential_key,created_at) VALUES (1001,'phase-c-target','active',1,1,1,'0123456789abcdef0123456789abcdef',CURRENT_TIMESTAMP),(1002,'phase-c-control','active',1,1,1,'fedcba9876543210fedcba9876543210',CURRENT_TIMESTAMP),(1003,'phase-c-online-target','active',1,1,1,'aabbccddeeff00112233445566778899',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed Phase C fixture: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO inbounds (id,tag) VALUES (1,?)`, phaseCWireGuardInbound); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hosts (id,remark,address,inbound_tag) VALUES (1,'Phase C E2E','127.0.0.1',?)`, phaseCWireGuardInbound); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id,name,address,port,api_port,status,last_status_change,certificate,certificate_key,node_capabilities) VALUES (42,'phase-c-node','127.0.0.1',?,10085,'connecting',datetime('now','-10 minutes'),?,?,'[]')`, servicePort, cert, key); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{
		"private_key": privateKey, "address_pool": "10.230.60.0/24", "server_address": "10.230.60.1/24",
		"tproxy_enabled": false, "nat_enabled": false,
	}
	if explicitInterface {
		settings["interface_name"] = interfaceName
		tagSum := sha256.Sum256([]byte(strings.TrimSpace(phaseCWireGuardInbound)))
		stateDir := filepath.Join(dataDir, "wireguard", "runtime", hex.EncodeToString(tagSum[:8]))
		if err := os.MkdirAll(stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		state, err := json.Marshal(map[string]any{"tag": phaseCWireGuardInbound, "interface_name": interfaceName, "explicit_interface": true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "runtime.json"), state, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": phaseCWireGuardInbound, "protocol": "wireguard", "port": wgPort,
			"settings": settings,
		}},
		"outbounds": []any{map[string]any{"tag": "DIRECT", "protocol": "freedom"}},
	}
	repo := xrayconfig.NewRepository(db, "sqlite", xrayconfig.Options{})
	if _, err := repo.SaveTargetRawConfig(ctx, xrayconfig.MasterTargetID, config); err != nil {
		t.Fatalf("save real WireGuard config: %v", err)
	}
}

func phaseCE2ECertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "phase-c-node"},
		DNSNames:     []string{"phase-c-node", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func phaseCLogin(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	form := url.Values{"username": {"phase-c-owner"}, "password": {"phase-c-e2e-local-password"}, "grant_type": {"password"}}
	resp, err := client.PostForm(baseURL+"/api/admin/token", form)
	if err != nil {
		t.Fatalf("real Panel authentication request: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != http.StatusOK || result.AccessToken == "" {
		t.Fatalf("real Panel authentication failed with status %d", resp.StatusCode)
	}
	return result.AccessToken
}

type phaseCProcess struct {
	cmd  *exec.Cmd
	file *os.File
}

func phaseCStartProcess(binary, dir, logPath string, values map[string]string) (*phaseCProcess, error) {
	return phaseCStartCommand(binary, nil, dir, logPath, values)
}

func phaseCStartCommand(binary string, args []string, dir, logPath string, values map[string]string) (*phaseCProcess, error) {
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = file, file
	cmd.Env = phaseCEnvironment(values)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &phaseCProcess{cmd: cmd, file: file}, nil
}

func phaseCEnvironment(values map[string]string) []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT"}
	env := make([]string, 0, len(keys)+len(values))
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func phaseCStopProcess(t *testing.T, process *phaseCProcess) {
	phaseCStopWithSignal(t, process, syscall.SIGTERM)
}

func phaseCStopWithSignal(t *testing.T, process *phaseCProcess, signal syscall.Signal) {
	t.Helper()
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-process.cmd.Process.Pid, signal)
	done := make(chan struct{})
	go func() { _ = process.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-process.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("process group %d did not exit after SIGKILL", process.cmd.Process.Pid)
		}
	}
	if process.file != nil {
		_ = process.file.Close()
	}
}

func phaseCCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func phaseCWait(t *testing.T, limit time.Duration, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, description)
}

func phaseCWireGuardPeers(interfaceName string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "wg", "show", interfaceName, "peers").CombinedOutput()
	if err != nil {
		return nil, err
	}
	peers := map[string]bool{}
	for _, peer := range strings.Fields(string(output)) {
		peers[strings.TrimSpace(peer)] = true
	}
	return peers, nil
}

func phaseCContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func phaseCWireGuardInterface(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("amwg%x", sum[:4])
}

func phaseCFreePort(t *testing.T, network string) int {
	t.Helper()
	var listener net.Listener
	var packet net.PacketConn
	var err error
	if network == "udp" {
		packet, err = net.ListenPacket("udp", "127.0.0.1:0")
		if err == nil {
			defer packet.Close()
			return packet.LocalAddr().(*net.UDPAddr).Port
		}
	} else {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			defer listener.Close()
			return listener.Addr().(*net.TCPAddr).Port
		}
	}
	t.Fatalf("allocate temporary %s port: %v", network, err)
	return 0
}

func requireE2EBinary(t *testing.T, variable string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(variable))
	if path == "" {
		t.Fatalf("%s must point to the built production executable", variable)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not an executable file", variable)
	}
	return path
}

func phaseCTail(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "<log unavailable>"
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	text := phaseCSanitize(string(data))
	return text
}

var (
	phaseCSensitiveAssignment = regexp.MustCompile(`(?i)(private[_ -]?key|credential[_ -]?key|token|authorization)(\s*[:=]\s*|\s+)[^\s,;]+`)
	phaseCBase64Secret        = regexp.MustCompile(`\b[A-Za-z0-9+/]{42,44}={0,2}\b`)
	phaseCHexSecret           = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	phaseCPEMSecret           = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
)

func phaseCSanitize(text string) string {
	text = phaseCPEMSecret.ReplaceAllString(text, "[REDACTED_PEM]")
	for _, marker := range []string{"PrivateKey = ", "Authorization: Bearer ", "Bearer "} {
		offset := 0
		for {
			index := strings.Index(text[offset:], marker)
			if index < 0 {
				break
			}
			start := offset + index + len(marker)
			end := strings.IndexAny(text[start:], " \r\n\t")
			if end < 0 {
				end = len(text) - start
			}
			text = text[:start] + "[REDACTED]" + text[start+end:]
			offset = start + len("[REDACTED]")
		}
	}
	text = phaseCSensitiveAssignment.ReplaceAllString(text, "${1}=[REDACTED]")
	text = phaseCBase64Secret.ReplaceAllString(text, "[REDACTED_BASE64]")
	return phaseCHexSecret.ReplaceAllString(text, "[REDACTED_HEX]")
}

func TestPhaseCEvidenceRedaction(t *testing.T) {
	input := "PrivateKey = key-one\nAuthorization: Bearer token-one\nBearer token-two\nprivate_key=key-two\n-----BEGIN PRIVATE KEY-----\nfixture-pem-data\n-----END PRIVATE KEY-----"
	output := phaseCSanitize(input)
	for _, secret := range []string{"key-one", "key-two", "token-one", "token-two", "fixture-pem-data"} {
		if strings.Contains(output, secret) {
			t.Fatalf("sanitized evidence still contains a fixture secret %q", secret)
		}
	}
	if !strings.Contains(output, "[REDACTED_PEM]") {
		t.Fatal("private-key PEM was not redacted")
	}
}

func phaseCPrintFailureEvidence(t *testing.T, db *sql.DB, panelLog, nodeLog, iface string, servicePort int, certPEM, keyPEM string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peers, wgErr := phaseCWireGuardPeers(iface)
	rows, queryErr := db.QueryContext(ctx, `SELECT id,operation_type,COALESCE(node_id,0),COALESCE(user_id,0),status,attempts,COALESCE(last_error,'') FROM node_operations ORDER BY id DESC LIMIT 12`)
	var operations []string
	if queryErr == nil {
		for rows.Next() {
			var id, nodeID, userID int64
			var kind, status, lastError string
			var attempts int
			if rows.Scan(&id, &kind, &nodeID, &userID, &status, &attempts, &lastError) == nil {
				operations = append(operations, fmt.Sprintf("id=%d type=%s node=%d user=%d status=%s attempts=%d error=%q", id, kind, nodeID, userID, status, attempts, phaseCSanitize(lastError)))
			}
		}
		_ = rows.Close()
	}
	t.Logf("sanitized Panel log tail:\n%s", phaseCTail(panelLog, 12000))
	t.Logf("sanitized Node log tail:\n%s", phaseCTail(nodeLog, 12000))
	t.Logf("WireGuard peer keys=%v command_error=%v", peers, wgErr)
	t.Logf("recent node operation summaries=%v query_error=%v", operations, queryErr)
	operationRows, operationErr := db.QueryContext(ctx, `SELECT id,state,phase,error FROM operations ORDER BY created_at DESC LIMIT 8`)
	if operationErr == nil {
		var summaries []string
		for operationRows.Next() {
			var id, state, phase, message string
			if operationRows.Scan(&id, &state, &phase, &message) == nil {
				summaries = append(summaries, fmt.Sprintf("id=%s state=%s phase=%s error=%q", id, state, phase, phaseCSanitize(message)))
			}
		}
		_ = operationRows.Close()
		t.Logf("recent sanitized operation events=%v", summaries)
	}
	t.Logf("sanitized live Node agent log tail:\n%s", phaseCNodeLogs(servicePort, certPEM, keyPEM))
}

func phaseCNodeLogs(servicePort int, certPEM, keyPEM string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tlsConfig, err := nodeclient.LoadClientTLSFromPEM(nodeclient.PEMTLSConfig{ClientCertPEM: certPEM, ClientKeyPEM: keyPEM, ServerCertPEM: certPEM})
	if err != nil {
		return "<TLS setup failed>"
	}
	client, err := nodeclient.Dial(ctx, fmt.Sprintf("127.0.0.1:%d", servicePort), tlsConfig, grpc.WithBlock())
	if err != nil {
		return "<Node logs unavailable>"
	}
	defer client.Close()
	stream, err := client.Logs().StreamLogs(ctx, &nodev1.StreamLogsRequest{StreamId: "phase-c-e2e-diagnostics", MaxLines: 100})
	if err != nil {
		return "<Node log stream unavailable>"
	}
	var lines []string
	for {
		line, err := stream.Recv()
		if err != nil {
			break
		}
		lines = append(lines, line.GetLine())
	}
	return phaseCSanitize(strings.Join(lines, "\n"))
}
