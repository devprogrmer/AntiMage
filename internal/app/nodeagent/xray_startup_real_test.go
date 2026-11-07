package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestRealXrayStartupBindFailure(t *testing.T) {
	xrayPath := os.Getenv("ANTIMAGE_XRAY_E2E_BINARY")
	if xrayPath == "" {
		t.Skip("set ANTIMAGE_XRAY_E2E_BINARY for real core startup checks")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	apiPort := realXrayFreeTCPPort(t)
	raw, err := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"api": map[string]any{"tag": "API", "services": []string{"StatsService"}}, "stats": map[string]any{},
		"inbounds": []any{
			map[string]any{"tag": "API_INBOUND", "listen": "127.0.0.1", "port": apiPort, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1"}},
			map[string]any{"tag": "occupied", "listen": "127.0.0.1", "port": occupied.Addr().(*net.TCPAddr).Port, "protocol": "vless", "settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "one"}}}},
		},
		"outbounds": []any{map[string]any{"protocol": "freedom"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"API_INBOUND"}, "outboundTag": "API"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{DataDir: t.TempDir(), XrayPath: xrayPath, XrayAssetsDir: filepath.Dir(xrayPath), XrayAPIPort: apiPort})
	t.Cleanup(func() { s.stopRuntime(); s.closeXrayStatsClient() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req := &nodev1.RuntimeConfigRequest{ConfigJson: string(raw), DesiredRevision: 7}
	s.xrayUsageMu.Lock()
	err = s.ensureXrayUsageStateLoadedLocked()
	if err == nil {
		s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "pending-before-bind-failure", Samples: []xrayUsageSample{{UserID: 1, InboundTag: "occupied", Value: 123}}}
		err = s.persistXrayUsageStateLocked()
	}
	s.xrayUsageMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The core accepts the configuration syntax; the failure occurs only when
	// starting the real listeners. A successful process spawn is not readiness.
	if err := s.validateXrayRuntimeConfig(ctx, req.ConfigJson); err != nil {
		t.Fatal(err)
	}
	response, err := s.SyncConfig(ctx, req)
	if err == nil || response.GetAccepted() {
		t.Fatal("bind failure was acknowledged as a successful runtime activation")
	}
	if s.runtimeState("check").GetAppliedRevision() != 0 || s.runtimeState("check").GetStarted() {
		t.Fatal("failed startup published a running/applied revision")
	}
	restarted := New(s.cfg)
	if err := restarted.ensureXrayUsageStateLoadedLocked(); err != nil || restarted.xrayUsagePending == nil || restarted.xrayUsagePending.BatchID != "pending-before-bind-failure" || restarted.xrayUsagePending.Samples[0].Value != 123 {
		t.Fatal("failed startup lost the durable unacknowledged usage receipt")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	response, err = s.SyncConfig(ctx, req)
	if err != nil || !response.GetAccepted() || response.GetRuntime().GetAppliedRevision() != 7 {
		t.Fatalf("retry after releasing port did not recover: %v %v", response, err)
	}
	client := newXrayStatsClient(xrayPath, apiPort)
	defer client.rpc.close()
	if _, err := client.queryStats(ctx, "antimage-startup-test", false); err != nil {
		t.Fatalf("activation was acknowledged before RPC readiness: %v", err)
	}

	// A second bind failure now has a confirmed configuration to recover.
	oldPort := occupied.Addr().(*net.TCPAddr).Port
	conflict, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	failedConfig := strings.Replace(req.ConfigJson, fmt.Sprintf(`"port":%d`, oldPort), fmt.Sprintf(`"port":%d`, conflict.Addr().(*net.TCPAddr).Port), 1)
	if failedConfig == req.ConfigJson {
		t.Fatal("test did not change the inbound port")
	}
	if response, err = s.SyncConfig(ctx, &nodev1.RuntimeConfigRequest{ConfigJson: failedConfig, DesiredRevision: 8}); err == nil || response.GetAccepted() {
		t.Fatal("failed update was accepted")
	}
	if runtime := s.runtimeState("check"); !runtime.GetStarted() || runtime.GetAppliedRevision() != 7 {
		t.Fatalf("previous revision did not recover: %v", runtime)
	}
	durable, err := New(s.cfg).loadRuntimePolicy()
	if err != nil || durable == nil || durable.Revision != 7 || durable.ConfigJSON != req.ConfigJson {
		t.Fatalf("previous revision was not retained for agent restart: %v", err)
	}
	conn, err := net.DialTimeout("tcp", occupied.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("previous inbound did not recover: %v", err)
	}
	conn.Close()

	if err := s.stopRuntime(); err != nil {
		t.Fatal(err)
	}
	s.closeXrayStatsClient()
	s = New(s.cfg)
	if err := s.restoreRuntimePolicy(ctx); err != nil || !s.runtimeState("check").GetStarted() {
		t.Fatalf("previous revision did not start on offline agent recovery: %v", err)
	}
	if err := conflict.Close(); err != nil {
		t.Fatal(err)
	}
	response, err = s.SyncConfig(ctx, &nodev1.RuntimeConfigRequest{ConfigJson: failedConfig, DesiredRevision: 8})
	if err != nil || response.GetRuntime().GetAppliedRevision() != 8 {
		t.Fatalf("released port did not apply: %v", err)
	}
	durable, err = New(s.cfg).loadRuntimePolicy()
	if err != nil || durable == nil || durable.Revision != 8 || durable.ConfigJSON != failedConfig {
		t.Fatalf("confirmed port change was not persisted: %v", err)
	}

	// The same rollback must not restore an account deleted by the panel.
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	blockedConfig := strings.Replace(req.ConfigJson, fmt.Sprintf(`"port":%d`, oldPort), fmt.Sprintf(`"port":%d`, blocked.Addr().(*net.TCPAddr).Port), 1)
	reduced := strings.Replace(blockedConfig, `[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}]`, "[]", 1)
	if reduced == blockedConfig {
		t.Fatal("test did not remove the prior account")
	}
	if _, err = s.SyncConfig(ctx, &nodev1.RuntimeConfigRequest{ConfigJson: reduced, DesiredRevision: 9}); err == nil {
		t.Fatal("bind failure with changed credentials was accepted")
	}
	if s.runtimeState("check").GetStarted() {
		t.Fatal("rollback restored a listener with a deleted credential")
	}
	durable, err = New(s.cfg).loadRuntimePolicy()
	if err != nil || durable == nil || durable.ConfigJSON != reduced {
		t.Fatalf("credential removal was not retained on disk: %v", err)
	}
}
