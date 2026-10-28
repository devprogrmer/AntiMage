package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// listVisibilitySchema mirrors the production nodes table plus the tls table the
// list query reads for default certificate material. Columns that the list scan
// reads are intentionally nullable so the test can prove a single node row with
// a NULL column never hides the rest of the list.
func listVisibilitySchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "nodes.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(context.Background(), `
CREATE TABLE tls (id INTEGER PRIMARY KEY, certificate TEXT, `+"`key`"+` TEXT);
CREATE TABLE nodes (
	id INTEGER PRIMARY KEY,
	name TEXT,
	note TEXT,
	address TEXT,
	port INTEGER,
	api_port INTEGER,
	usage_coefficient REAL,
	data_limit INTEGER,
	proxy_enabled INTEGER,
	proxy_type TEXT,
	proxy_host TEXT,
	proxy_port INTEGER,
	proxy_username TEXT,
	proxy_password TEXT,
	status TEXT,
	agent_status TEXT,
	xray_status TEXT,
	desired_revision INTEGER,
	applied_revision INTEGER,
	node_capabilities TEXT,
	message TEXT,
	xray_version TEXT,
	node_binary_tag TEXT,
	geo_mode TEXT,
	xray_config_mode TEXT,
	uplink INTEGER,
	downlink INTEGER,
	certificate TEXT,
	certificate_key TEXT
)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestListNodeItemsIncludesEveryNonDeletedNode proves list membership is driven
// only by soft-delete status: connectivity, heartbeat, capabilities, and running
// state must never remove a node from the list.
func TestListNodeItemsIncludesEveryNonDeletedNode(t *testing.T) {
	ctx := context.Background()
	db := listVisibilitySchema(t)
	if _, err := db.ExecContext(ctx, `
INSERT INTO nodes (id, name, address, port, api_port, usage_coefficient, status, agent_status, xray_status, node_capabilities, geo_mode, xray_config_mode, uplink, downlink, proxy_enabled) VALUES
	(1,'never-connected','192.0.2.1',62050,62051,1,'connecting','unknown','unknown','[]','default','default',0,0,0),
	(2,'connected','192.0.2.2',62050,62051,1,'connected','connected','running','[]','default','default',0,0,0),
	(3,'disconnected','192.0.2.3',62050,62051,1,'connecting','unknown','unknown','[]','default','default',0,0,0),
	(4,'failed-handshake','192.0.2.4',62050,62051,1,'error','error','stopped','[]','default','default',0,0,0),
	(5,'stale-heartbeat','192.0.2.5',62050,62051,1,'error','degraded','unknown','[]','default','default',0,0,0),
	(6,'missing-capabilities','192.0.2.6',62050,62051,1,'connecting','unknown','unknown',NULL,'default','default',0,0,0),
	(7,'deleted','192.0.2.7',62050,62051,1,'deleted','unknown','unknown','[]','default','default',0,0,0)`); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db, "sqlite")
	rows, _, _, err := repo.ListNodeItems(ctx, 0)
	if err != nil {
		t.Fatalf("ListNodeItems: %v", err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Name] = row.Status
	}
	want := map[string]string{
		"never-connected":      "connecting",
		"connected":            "connected",
		"disconnected":         "connecting",
		"failed-handshake":     "error",
		"stale-heartbeat":      "error",
		"missing-capabilities": "connecting",
	}
	for name, status := range want {
		if got[name] != status {
			t.Fatalf("node %q status = %q, want %q (seen %#v)", name, got[name], status, got)
		}
	}
	if _, ok := got["deleted"]; ok {
		t.Fatalf("soft-deleted node leaked into list: %#v", got)
	}
}

// TestListNodeItemsSurvivesNullColumns reproduces the production Nodes-page
// outage: a single node row with a NULL scanned column must not fail the whole
// list request and hide every other node.
func TestListNodeItemsSurvivesNullColumns(t *testing.T) {
	ctx := context.Background()
	db := listVisibilitySchema(t)
	if _, err := db.ExecContext(ctx, `
INSERT INTO nodes (id, name, status) VALUES
	(1,'null-status',NULL),
	(2,'null-address-and-port',NULL),
	(3,'healthy','connecting')`); err != nil {
		t.Fatal(err)
	}
	// Give the NULL-address node a name but leave address/port NULL; the row is
	// exactly what a partially written node record looks like.
	if _, err := db.ExecContext(ctx, `UPDATE nodes SET name='null-everything' WHERE id=2`); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db, "sqlite")
	rows, _, _, err := repo.ListNodeItems(ctx, 0)
	if err != nil {
		t.Fatalf("ListNodeItems failed on NULL columns: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected all rows visible, got %d: %#v", len(rows), rows)
	}
	statuses := map[string]string{}
	for _, row := range rows {
		statuses[row.Name] = row.Status
	}
	if statuses["healthy"] != "connecting" {
		t.Fatalf("healthy node hidden by NULL rows: %#v", statuses)
	}
	if statuses["null-status"] != "connecting" {
		t.Fatalf("NULL status should normalize to connecting: %#v", statuses)
	}
}
