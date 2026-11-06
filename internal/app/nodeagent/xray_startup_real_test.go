package nodeagent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
}
