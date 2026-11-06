package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestRealXrayLiveUserAdditions(t *testing.T) {
	xrayPath := os.Getenv("ANTIMAGE_XRAY_E2E_BINARY")
	if xrayPath == "" {
		t.Skip("set ANTIMAGE_XRAY_E2E_BINARY for real-core witness traffic tests")
	}
	for _, variant := range []struct{ name, protocol, method string }{{"vless", "vless", ""}, {"vmess", "vmess", ""}, {"trojan", "trojan", ""}, {"shadowsocks-aead", "shadowsocks", "aes-128-gcm"}, {"shadowsocks-2022", "shadowsocks", "2022-blake3-aes-128-gcm"}, {"hysteria-2", "hysteria", ""}} {
		t.Run(variant.name, func(t *testing.T) {
			protocol := variant.protocol
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echo.Close()
			go func() {
				for {
					conn, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer conn.Close(); io.Copy(conn, conn) }()
				}
			}()
			echoPort := echo.Addr().(*net.TCPAddr).Port
			apiPort, serverPort := realXrayFreeTCPPort(t), realXrayFreeTCPPort(t)
			for apiPort == serverPort || apiPort == echoPort || serverPort == echoPort {
				apiPort, serverPort = realXrayFreeTCPPort(t), realXrayFreeTCPPort(t)
			}
			oldID, newID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			serverKey := "AAAAAAAAAAAAAAAAAAAAAA=="
			if strings.HasPrefix(variant.method, "2022-") {
				oldID, newID = "AQEBAQEBAQEBAQEBAQEBAQ==", "AgICAgICAgICAgICAgICAg=="
			} else if protocol == "hysteria" {
				oldID, newID = "11111111111141118111111111111111", "22222222222242228222222222222222"
			}
			var hysteriaServerStream map[string]any
			var hysteriaPeerFingerprint string
			if protocol == "hysteria" {
				certificate, certPEM, keyPEM := nodeTLSCertificate(t, "localhost")
				fingerprint := sha256.Sum256(certificate.Certificate[0])
				hysteriaPeerFingerprint = hex.EncodeToString(fingerprint[:])
				certDir := t.TempDir()
				certPath, keyPath := filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem")
				if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
					t.Fatal(err)
				}
				hysteriaServerStream = map[string]any{"network": "hysteria", "security": "tls", "hysteriaSettings": map[string]any{"version": 2, "auth": "shared-server-secret"}, "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": certPath, "keyFile": keyPath}}}}
			}
			clientCredential := func(id, email string) map[string]any {
				field := "id"
				if protocol == "trojan" || protocol == "shadowsocks" {
					field = "password"
				} else if protocol == "hysteria" {
					field = "auth"
				}
				client := map[string]any{field: id, "email": email, "level": 0}
				if protocol == "shadowsocks" && !strings.HasPrefix(variant.method, "2022-") {
					client["method"] = variant.method
					client["iv_check"] = false
				}
				return client
			}
			config := func(includeNew bool) string {
				clients := []any{clientCredential(oldID, "1.witness")}
				if includeNew {
					clients = append(clients, clientCredential(newID, "2.new-user"))
				}
				settings := map[string]any{"clients": clients}
				if protocol == "vless" {
					settings["decryption"] = "none"
				}
				if strings.HasPrefix(variant.method, "2022-") {
					settings["method"] = variant.method
					settings["password"] = serverKey
				} else if protocol == "hysteria" {
					settings["version"] = 2
				}
				userInbound := map[string]any{"tag": "users", "listen": "127.0.0.1", "port": serverPort, "protocol": protocol, "settings": settings}
				if protocol == "hysteria" {
					userInbound["streamSettings"] = hysteriaServerStream
				}
				raw, err := json.Marshal(map[string]any{
					"log": map[string]any{"loglevel": "warning"}, "api": map[string]any{"tag": "API", "services": []string{"HandlerService", "StatsService"}}, "stats": map[string]any{},
					"policy":    map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}},
					"inbounds":  []any{map[string]any{"tag": "API_INBOUND", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1"}}, userInbound},
					"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow"}}}}},
					"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"API_INBOUND"}, "outboundTag": "API"}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			native := func(includeNew bool) string {
				policies := []xrayLocalUserPolicy{{UserID: 1, Email: "1.witness", InboundTag: "users", Protocol: protocol, nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active"}}}
				if includeNew {
					policies = append(policies, xrayLocalUserPolicy{UserID: 2, Email: "2.new-user", InboundTag: "users", Protocol: protocol, nativeSessionUserPolicy: nativeSessionUserPolicy{Status: "active"}})
				}
				raw, err := json.Marshal(nativeRuntimePayload{XrayPolicies: policies})
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			s := New(Config{DataDir: t.TempDir(), XrayPath: xrayPath, XrayAssetsDir: filepath.Dir(xrayPath), XrayAPIPort: apiPort})
			t.Cleanup(func() {
				s.xrayTransitionMu.Lock()
				s.stopRuntime()
				s.xrayTransitionMu.Unlock()
				s.closeXrayStatsClient()
			})
			if _, err := s.SyncConfig(ctx, &nodev1.RuntimeConfigRequest{ConfigJson: config(false), OvRuntimeJson: native(false), DesiredRevision: 1}); err != nil {
				t.Fatal(err)
			}
			realXrayWaitForTCP(t, apiPort, &realXrayLogBuffer{})
			s.mu.Lock()
			pid, generation := s.lastRuntime.Process.Pid, s.xrayRuntimeGeneration
			s.mu.Unlock()
			startClient := func(id string) net.Conn {
				t.Helper()
				port := realXrayFreeTCPPort(t)
				var settings map[string]any
				if protocol == "hysteria" {
					settings = map[string]any{"version": 2, "address": "127.0.0.1", "port": serverPort}
				} else if protocol == "trojan" || protocol == "shadowsocks" {
					server := map[string]any{"address": "127.0.0.1", "port": serverPort, "password": id}
					if protocol == "shadowsocks" {
						server["method"] = variant.method
						if strings.HasPrefix(variant.method, "2022-") && id != "" {
							server["password"] = serverKey + ":" + id
						} else if strings.HasPrefix(variant.method, "2022-") {
							server["password"] = serverKey
						}
					}
					settings = map[string]any{"servers": []any{server}}
				} else {
					user := map[string]any{"id": id}
					if protocol == "vless" {
						user["encryption"] = "none"
					} else {
						user["security"] = "auto"
					}
					settings = map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": serverPort, "users": []any{user}}}}
				}
				outbound := map[string]any{"protocol": protocol, "settings": settings}
				if protocol == "hysteria" {
					outbound["streamSettings"] = map[string]any{"network": "hysteria", "security": "tls", "hysteriaSettings": map[string]any{"version": 2, "auth": id}, "tlsSettings": map[string]any{"pinnedPeerCertSha256": hysteriaPeerFingerprint, "serverName": "localhost"}}
				}
				raw, err := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1", "port": echoPort, "network": "tcp"}}}, "outbounds": []any{outbound}})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "client.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				process := realXrayStartProcess(t, xrayPath, path)
				realXrayWaitForTCP(t, port, process.logs)
				conn := realXrayDialUntilReady(t, port, process.logs)
				t.Cleanup(func() { conn.Close() })
				return conn
			}
			exchange := func(conn net.Conn, sequence string) error {
				conn.SetDeadline(time.Now().Add(4 * time.Second))
				payload := []byte("antimage-live-witness-" + sequence)
				if _, err := conn.Write(payload); err != nil {
					return err
				}
				reply := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, reply); err != nil {
					return err
				}
				if string(reply) != string(payload) {
					return fmt.Errorf("tunneled data changed")
				}
				return nil
			}
			ping := func(conn net.Conn, sequence string) {
				t.Helper()
				if err := exchange(conn, sequence); err != nil {
					t.Fatal(err)
				}
			}
			witness := startClient(oldID)
			ping(witness, "before")
			req := &nodev1.RuntimeConfigRequest{ConfigJson: config(true), OvRuntimeJson: native(true), DesiredRevision: 2}
			for i := 0; i < 2; i++ {
				if _, err := s.SyncConfig(ctx, req); err != nil {
					t.Fatal(err)
				}
				ping(witness, fmt.Sprint("after-retry-", i))
			}
			newUser := startClient(newID)
			ping(newUser, "new-user")
			ping(witness, "after-new-user")
			s.mu.Lock()
			same := s.lastRuntime != nil && s.lastRuntime.Process.Pid == pid && s.xrayRuntimeGeneration == generation
			s.mu.Unlock()
			if !same {
				t.Fatal("adding/retrying user replaced the witness core")
			}
			if protocol == "vless" {
				invalid := proto.Clone(req).(*nodev1.RuntimeConfigRequest)
				invalid.DesiredRevision = 3
				invalid.ConfigJson = strings.Replace(req.ConfigJson, `"protocol":"vless"`, `"protocol":"invalid-protocol"`, 1)
				if _, err := s.SyncConfig(ctx, invalid); err == nil {
					t.Fatal("target core accepted an invalid replacement config")
				}
				ping(witness, "after-invalid-config")
				ping(newUser, "after-invalid-config")
				s.mu.Lock()
				retained := s.lastRuntime != nil && s.lastRuntime.Process.Pid == pid && s.xrayRuntimeGeneration == generation && s.appliedRev == 2
				s.mu.Unlock()
				if !retained {
					t.Fatal("invalid config replaced the running core or advanced its revision")
				}
			}
			// Recovery must keep the authoritative new credential, not only RAM.
			restarted := New(s.cfg)
			policy, err := restarted.loadRuntimePolicy()
			if err != nil || policy.ConfigJSON != req.ConfigJson || policy.Revision != 2 {
				t.Fatal("new user missing from durable restart policy")
			}
			if err := s.checkpointXrayAccounting(ctx); err != nil {
				t.Fatal(err)
			}
			pending, err := s.collectXrayUserUsage(ctx, nil)
			if err != nil || pending.BatchId == "" {
				t.Fatalf("capture pending usage before restart: %v %v", pending, err)
			}
			s.xrayTransitionMu.Lock()
			err = s.stopRuntime()
			s.xrayTransitionMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", apiPort), 100*time.Millisecond)
				if dialErr != nil {
					break
				}
				conn.Close()
				if time.Now().After(deadline) {
					t.Fatal("old core did not exit before restart")
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Cleanup(func() {
				restarted.xrayTransitionMu.Lock()
				restarted.stopRuntime()
				restarted.xrayTransitionMu.Unlock()
				restarted.closeXrayStatsClient()
			})
			if err := restarted.restoreRuntimePolicy(ctx); err != nil {
				t.Fatal(err)
			}
			realXrayWaitForTCP(t, apiPort, &realXrayLogBuffer{})
			recoveredClient := startClient(newID)
			ping(recoveredClient, "restored-user")
			replay, err := restarted.collectXrayUserUsage(ctx, nil)
			if err != nil || replay.BatchId != pending.BatchId || len(replay.Stats) != len(pending.Stats) {
				t.Fatalf("restart lost pending receipt: %v %v", replay, err)
			}
			for i := range pending.Stats {
				if !proto.Equal(pending.Stats[i], replay.Stats[i]) {
					t.Fatal("restart changed unacknowledged usage")
				}
			}
			t.Logf("protocol=%s witness PID=%d generation retained during additions; new user authenticated; retry, actual restart authentication and unchanged pending receipt passed", protocol, pid)
			if strings.HasPrefix(variant.method, "2022-") || protocol == "hysteria" {
				fallbackID := ""
				if protocol == "hysteria" {
					fallbackID = "shared-server-secret"
				}
				if err := exchange(startClient(fallbackID), "fallback-before-quota"); err == nil {
					t.Fatal("server credential accepted while managed users are active")
				}
				stopAndWait := func(server *Server) {
					t.Helper()
					server.xrayTransitionMu.Lock()
					err := server.stopRuntime()
					server.xrayTransitionMu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
					deadline := time.Now().Add(4 * time.Second)
					for {
						conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", apiPort), 100*time.Millisecond)
						if err != nil {
							return
						}
						conn.Close()
						if time.Now().After(deadline) {
							t.Fatal("core did not exit after stop")
						}
						time.Sleep(20 * time.Millisecond)
					}
				}
				stopAndWait(restarted)
				for _, mode := range []string{"quota-filter", "offline-restart"} {
					t.Run(mode, func(t *testing.T) {
						denied := New(Config{DataDir: t.TempDir(), XrayPath: xrayPath, XrayAssetsDir: filepath.Dir(xrayPath), XrayAPIPort: apiPort})
						defer func() { stopAndWait(denied); denied.closeXrayStatsClient() }()
						blocked := &nodev1.RuntimeConfigRequest{ConfigJson: config(true), DesiredRevision: 3}
						if mode == "quota-filter" {
							var removed bool
							blocked.ConfigJson, removed, err = removeXrayConfigClients(blocked.ConfigJson, map[string]struct{}{"1.witness": {}, "2.new-user": {}})
							if err != nil || !removed {
								t.Fatalf("quota filter: removed=%v error=%v", removed, err)
							}
						} else {
							blocked.OvRuntimeJson = strings.ReplaceAll(native(true), `"status":"active"`, `"status":"limited"`)
							if err := denied.persistRuntimePolicy(blocked); err != nil {
								t.Fatal(err)
							}
							denied = New(denied.cfg)
						}
						if mode == "offline-restart" {
							err = denied.restoreRuntimePolicy(ctx)
						} else {
							_, err = denied.SyncConfig(ctx, blocked)
						}
						if err != nil {
							t.Fatal(err)
						}
						realXrayWaitForTCP(t, apiPort, &realXrayLogBuffer{})
						if err := exchange(startClient(fallbackID), "fallback-after-quota"); err == nil {
							t.Fatal("QUOTA BYPASS: server credential authenticated after the last managed user was revoked")
						}
						if err := exchange(startClient(newID), "revoked-after-quota"); err == nil {
							t.Fatal("revoked managed credential still authenticated")
						}
					})
				}
			}
		})
	}
}
