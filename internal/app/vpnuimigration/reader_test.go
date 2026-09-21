package vpnuimigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAnalyzeCurrentVPNUIAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE accounts (
 id INTEGER PRIMARY KEY, email TEXT, total_gb INTEGER, expiry_time INTEGER,
 enable BOOLEAN, limit_ip INTEGER, user_limit_override INTEGER, comment TEXT
);
CREATE TABLE client_traffics (email TEXT, up INTEGER, down INTEGER);
INSERT INTO accounts VALUES (7, 'alice', 1048576, 2000000000000, 1, 2, 3, 'from vpn-ui');
INSERT INTO client_traffics VALUES ('alice', 100, 250);
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	analysis, err := Analyze(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(analysis.Accounts))
	}
	got := analysis.Accounts[0]
	if got.Username != "alice" || got.UsedBytes != 350 || got.IPLimit != 2 || got.DeviceLimit != 3 {
		t.Fatalf("unexpected account: %#v", got)
	}
	if got.DataLimit == nil || *got.DataLimit != 1048576 {
		t.Fatalf("data limit = %#v", got.DataLimit)
	}
	if got.Expire == nil || *got.Expire != 2000000000 {
		t.Fatalf("expire = %#v", got.Expire)
	}
}

func TestAnalyzeLegacyVPNUIClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE client_traffics (
 id INTEGER PRIMARY KEY, email TEXT, enable BOOLEAN, up INTEGER, down INTEGER,
 expiry_time INTEGER, total INTEGER
); INSERT INTO client_traffics VALUES (1, 'legacy', 0, 10, 20, 0, 0);`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	analysis, err := Analyze(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Accounts) != 1 || analysis.Accounts[0].Enabled || analysis.Accounts[0].UsedBytes != 30 {
		t.Fatalf("unexpected legacy analysis: %#v", analysis)
	}
	if len(analysis.Warnings) == 0 {
		t.Fatal("expected legacy warning")
	}
}

func TestAnalyzeRejectsUnrelatedSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE unrelated (id INTEGER PRIMARY KEY)`)
	_ = db.Close()
	if _, err := Analyze(context.Background(), path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}
