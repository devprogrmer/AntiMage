//go:build cgo

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/diagnostics"
	systemapp "github.com/antimage/antimage/internal/app/system"
)

type diagnosticsMetricsProvider struct {
	diskPercent, memoryPercent float64
}

func (p *diagnosticsMetricsProvider) Snapshot(context.Context) (systemapp.MetricsSnapshot, error) {
	return systemapp.MetricsSnapshot{
		Timestamp: time.Now().Unix(), CPUCores: 2, CPUThreads: 2,
		Memory: systemapp.UsageStats{Current: 30, Total: 100, Percent: p.memoryPercent},
		Disk:   systemapp.UsageStats{Current: 20, Total: 100, Percent: p.diskPercent},
	}, nil
}

func TestDiagnosticsCollectorRefreshLifecycle(t *testing.T) {
	server, db := testAdminServer(t)
	insertMasterAPIAdmin(t, db, 1, "owner", "pass123", adminapp.RoleFullAccess, adminapp.StatusActive)
	validConfig := `{"inbounds":[{"tag":"vless-tcp","port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp","security":"tls"}}],"outbounds":[{"tag":"DIRECT","protocol":"freedom"}]}`
	statements := []string{
		`CREATE TABLE system (id INTEGER PRIMARY KEY, uplink BIGINT DEFAULT 0, downlink BIGINT DEFAULT 0)`,
		`ALTER TABLE users ADD COLUMN used_traffic BIGINT DEFAULT 0`,
		`CREATE TABLE user_online_ips (node_id BIGINT, user_id BIGINT, last_seen_at DATETIME)`,
		`CREATE TABLE vpn_user_sessions (node_id BIGINT, user_id BIGINT, last_seen_at DATETIME, ended_at DATETIME)`,
		`CREATE TABLE diagnostics (id TEXT PRIMARY KEY, source TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, severity TEXT NOT NULL, code TEXT NOT NULL, summary TEXT NOT NULL, detail TEXT NOT NULL, first_seen_at BIGINT NOT NULL, last_seen_at BIGINT NOT NULL, occurrence_count BIGINT NOT NULL, status TEXT NOT NULL, recommended_action TEXT NOT NULL, UNIQUE(source,resource_type,resource_id,code))`,
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,requested_by TEXT NOT NULL,request_id TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,progress INTEGER NULL,created_at BIGINT NOT NULL,started_at BIGINT NULL,updated_at BIGINT NOT NULL,completed_at BIGINT NULL,error TEXT NOT NULL,metadata_json TEXT NOT NULL)`,
		`CREATE TABLE subscription_domains (id INTEGER PRIMARY KEY, domain VARCHAR(255) NOT NULL, admin_id INTEGER NULL, email VARCHAR(255) NULL, provider VARCHAR(64) NULL, alt_names TEXT NULL, last_issued_at DATETIME NULL, last_renewed_at DATETIME NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(domain))`,
		`CREATE TABLE telegram_settings (id INTEGER PRIMARY KEY, api_token TEXT NULL, use_telegram INTEGER DEFAULT 1, proxy_url TEXT NULL, admin_chat_ids TEXT DEFAULT '[]', logs_chat_id BIGINT NULL, logs_chat_is_forum INTEGER DEFAULT 0, backup_chat_id BIGINT NULL, backup_chat_is_forum INTEGER DEFAULT 0, default_vless_flow TEXT NULL, forum_topics TEXT DEFAULT '{}', event_toggles TEXT DEFAULT '{}', backup_enabled INTEGER DEFAULT 0, backup_scope TEXT DEFAULT 'database', backup_interval_value INTEGER DEFAULT 24, backup_interval_unit TEXT DEFAULT 'hours', backup_last_sent_at DATETIME NULL, backup_last_error TEXT NULL, last_sent_at DATETIME NULL, last_error TEXT NULL, last_error_at DATETIME NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO xray_config (id, data) VALUES (1, '` + validConfig + `')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
	provider := &diagnosticsMetricsProvider{diskPercent: 20, memoryPercent: 30}
	server.systemService = systemapp.NewServiceWithProvider(db, "sqlite", systemapp.DefaultVersion, provider)
	token := adminBearerToken(t, server, "owner", "pass123")

	refresh := func() *httptest.ResponseRecorder {
		t.Helper()
		return adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/diagnostics?refresh=1&status=active", token, "")
	}
	decode := func(rec *httptest.ResponseRecorder) []diagnostics.Record {
		t.Helper()
		var response struct {
			Diagnostics []diagnostics.Record `json:"diagnostics"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode diagnostics: %v; body=%s", err, rec.Body.String())
		}
		return response.Diagnostics
	}

	if rec := refresh(); rec.Code != http.StatusOK {
		t.Fatalf("healthy refresh status=%d body=%s", rec.Code, rec.Body.String())
	} else if items := decode(rec); len(items) != 0 {
		t.Fatalf("healthy refresh produced false findings: %+v", items)
	}
	if _, err := db.Exec(`UPDATE xray_config SET data=? WHERE id=1`, `{"password":"hunter2","inbounds":[],"outbounds":[]}`); err != nil {
		t.Fatal(err)
	}
	redacted := refresh()
	if redacted.Code != http.StatusOK || strings.Contains(redacted.Body.String(), "hunter2") {
		t.Fatalf("invalid config leaked secret or failed to refresh: status=%d body=%s", redacted.Code, redacted.Body.String())
	}
	var storedDetail string
	if err := db.QueryRow(`SELECT detail FROM diagnostics WHERE code='xray.config_invalid'`).Scan(&storedDetail); err != nil || strings.Contains(storedDetail, "hunter2") {
		t.Fatalf("stored diagnostic leaked config secret: detail=%q err=%v", storedDetail, err)
	}
	if _, err := db.Exec(`UPDATE xray_config SET data=? WHERE id=1`, validConfig); err != nil {
		t.Fatal(err)
	}
	if rec := refresh(); rec.Code != http.StatusOK {
		t.Fatalf("config recovery refresh status=%d body=%s", rec.Code, rec.Body.String())
	}

	provider.diskPercent = 95
	first := refresh()
	if first.Code != http.StatusOK {
		t.Fatalf("critical refresh status=%d body=%s", first.Code, first.Body.String())
	}
	items := decode(first)
	if len(items) != 1 || items[0].Code != "disk.pressure" || items[0].Severity != "critical" || items[0].OccurrenceCount != 1 {
		t.Fatalf("critical refresh diagnostics=%+v", items)
	}
	firstSeen, lastSeen := items[0].FirstSeenAt, items[0].LastSeenAt
	time.Sleep(1100 * time.Millisecond)
	second := refresh()
	if second.Code != http.StatusOK {
		t.Fatalf("repeat refresh status=%d body=%s", second.Code, second.Body.String())
	}
	items = decode(second)
	if len(items) != 1 || items[0].OccurrenceCount != 1 || items[0].FirstSeenAt != firstSeen || items[0].LastSeenAt <= lastSeen {
		t.Fatalf("repeat refresh did not preserve lifecycle timestamps/count: %+v", items)
	}
	id := items[0].ID
	ack := adminJSONRequest(t, server, http.MethodPost, "/api/maintenance/diagnostics/"+id+"/acknowledge", token, "{}")
	if ack.Code != http.StatusNoContent {
		t.Fatalf("acknowledge status=%d body=%s", ack.Code, ack.Body.String())
	}
	if rec := adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/diagnostics?status=acknowledged", token, ""); rec.Code != http.StatusOK {
		t.Fatalf("acknowledged diagnostics read status=%d body=%s", rec.Code, rec.Body.String())
	} else if got := decode(rec); len(got) != 1 || got[0].Status != "acknowledged" {
		t.Fatalf("refresh lost acknowledgement: %+v", got)
	}
	if rec := refresh(); rec.Code != http.StatusOK {
		t.Fatalf("acknowledged refresh status=%d body=%s", rec.Code, rec.Body.String())
	}

	provider.diskPercent = 20
	recovered := refresh()
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovery refresh status=%d body=%s", recovered.Code, recovered.Body.String())
	}
	items = decode(recovered)
	if len(items) != 0 {
		t.Fatalf("recovered finding remained active: %+v", items)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM diagnostics WHERE id=?`, id).Scan(&status); err != nil || status != "resolved" {
		t.Fatalf("stored status=%q err=%v", status, err)
	}

	if _, err := db.Exec(`DROP TABLE node_operations`); err != nil {
		t.Fatal(err)
	}
	failed := refresh()
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed-source refresh status=%d body=%s", failed.Code, failed.Body.String())
	}
	if err := db.QueryRow(`SELECT status FROM diagnostics WHERE id=?`, id).Scan(&status); err != nil || status != "resolved" {
		t.Fatalf("source failure changed existing finding: status=%q err=%v", status, err)
	}
}

func TestDiagnosticsRouteRequiresFullAccess(t *testing.T) {
	server, db := testAdminServer(t)
	insertMasterAPIAdmin(t, db, 1, "seller", "pass123", adminapp.RoleStandard, adminapp.StatusActive)
	token := adminBearerToken(t, server, "seller", "pass123")
	rec := adminJSONRequest(t, server, http.MethodGet, "/api/maintenance/diagnostics", token, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("standard-admin diagnostics status=%d body=%s", rec.Code, rec.Body.String())
	}
}
