package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
	_ "modernc.org/sqlite"
)

func TestAccountingInboxPersistsAndOnlyWritesOnTransitions(t *testing.T) {
	db, err := sql.Open("sqlite", "file:accounting-inbox-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE node_accounting_alerts (node_id BIGINT NOT NULL, protocol TEXT NOT NULL, kind TEXT NOT NULL, node_name TEXT NOT NULL, opened_at BIGINT NOT NULL, resolved_at BIGINT NULL, read_at BIGINT NULL, PRIMARY KEY (node_id, protocol, kind))`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Unix(1000, 0)
	alert := nodecontroller.AccountingAlert{NodeID: 7, NodeName: "node-a", Protocol: "xray", Kind: "checkpoint_stale"}
	if err := syncAccountingInbox(ctx, db, []int64{7}, []nodecontroller.AccountingAlert{alert}, start); err != nil {
		t.Fatal(err)
	}
	// A database trigger turns any unnecessary UPDATE into a test failure.
	_, err = db.Exec(`CREATE TRIGGER forbid_unchanged_alert_update BEFORE UPDATE ON node_accounting_alerts WHEN OLD.resolved_at IS NULL AND NEW.resolved_at IS NULL AND NEW.read_at IS OLD.read_at BEGIN SELECT RAISE(FAIL, 'unchanged alert was rewritten'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncAccountingInbox(ctx, db, []int64{7}, []nodecontroller.AccountingAlert{alert}, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := syncAccountingInbox(ctx, db, nil, nil, start.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var opened int64
	var resolved sql.NullInt64
	if err := db.QueryRow(`SELECT opened_at, resolved_at FROM node_accounting_alerts WHERE node_id = 7`).Scan(&opened, &resolved); err != nil {
		t.Fatal(err)
	}
	if opened != 1000 || resolved.Valid {
		t.Fatalf("sampled alert changed without a transition: opened=%d resolved=%v", opened, resolved)
	}
	// A successful healthy sample resolves the alert and survives a new reader.
	if err := syncAccountingInbox(ctx, db, []int64{7}, nil, start.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT resolved_at FROM node_accounting_alerts WHERE node_id = 7`).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if !resolved.Valid || resolved.Int64 != 1180 {
		t.Fatalf("resolved_at=%v", resolved)
	}
	if err := syncAccountingInbox(ctx, db, []int64{7}, []nodecontroller.AccountingAlert{alert}, start.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT opened_at, resolved_at FROM node_accounting_alerts WHERE node_id = 7`).Scan(&opened, &resolved); err != nil {
		t.Fatal(err)
	}
	if opened != 1240 || resolved.Valid {
		t.Fatalf("reopened alert: opened=%d resolved=%v", opened, resolved)
	}
	server := &Server{db: db}
	list := httptest.NewRecorder()
	server.handleAccountingInbox(list, httptest.NewRequest(http.MethodGet, "/api/nodes/accounting-alerts", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var items []accountingInboxItem
	if err := json.Unmarshal(list.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ReadAt != nil || items[0].OpenedAt != 1240 {
		t.Fatalf("restarted reader lost alert: %+v", items)
	}
	read := httptest.NewRecorder()
	server.handleAccountingInboxRead(read, httptest.NewRequest(http.MethodPost, "/api/nodes/accounting-alerts/read", strings.NewReader(`{"node_id":7,"protocol":"xray","kind":"checkpoint_stale"}`)))
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	list = httptest.NewRecorder()
	server.handleAccountingInbox(list, httptest.NewRequest(http.MethodGet, "/api/nodes/accounting-alerts", nil))
	items = nil
	if err := json.Unmarshal(list.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ReadAt == nil {
		t.Fatalf("read state was not persisted: %+v", items)
	}
	if err := syncAccountingInbox(ctx, db, []int64{7}, nil, start.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := syncAccountingInbox(ctx, db, []int64{7}, []nodecontroller.AccountingAlert{alert}, start.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list = httptest.NewRecorder()
	server.handleAccountingInbox(list, httptest.NewRequest(http.MethodGet, "/api/nodes/accounting-alerts", nil))
	items = nil
	if err := json.Unmarshal(list.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ReadAt != nil || items[0].OpenedAt != 1360 {
		t.Fatalf("new incident must be unread: %+v", items)
	}
}
