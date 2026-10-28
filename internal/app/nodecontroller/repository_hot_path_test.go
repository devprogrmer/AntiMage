package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	userread "github.com/antimage/antimage/internal/app/user"

	_ "modernc.org/sqlite"
)

func openRepositoryHotPathDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "hot-path.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// runtimeUsers interns the decoded proxy settings map across rows that share the
// same JSON payload. That only stays correct while every consumer treats the
// returned map as read-only, so lock the invariant in place: the settings maps
// for rows that share a payload must be equal, and running the real settings
// reader over them must not mutate the shared state.
func TestRuntimeUserSettingsInterningIsReadOnly(t *testing.T) {
	ctx := context.Background()
	db := openRepositoryHotPathDB(t, `
CREATE TABLE users (
	id INTEGER PRIMARY KEY,
	username TEXT,
	credential_key TEXT,
	flow TEXT,
	service_id INTEGER,
	status TEXT,
	ip_limit INTEGER DEFAULT 0
);
CREATE TABLE proxies (id INTEGER PRIMARY KEY, user_id INTEGER, type TEXT, settings TEXT);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled INTEGER DEFAULT 0);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
INSERT INTO users (id, username, credential_key, service_id, status) VALUES
	(1, 'shared-a', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 10, 'active'),
	(2, 'shared-b', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 10, 'active');
INSERT INTO proxies (user_id, type, settings) VALUES
	(1, 'vless', '{}'),
	(2, 'vless', '{}');
`)

	repo := NewRepository(db, "sqlite")
	rows, err := repo.RuntimeUsersForProtocols(ctx, []string{"vless"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two runtime rows, got %d", len(rows))
	}

	// Rows that share a payload must decode to equal settings.
	if !reflect.DeepEqual(rows[0].Settings, rows[1].Settings) {
		t.Fatalf("rows sharing a settings payload decoded differently: %#v vs %#v", rows[0].Settings, rows[1].Settings)
	}
	before := make([]map[string]any, len(rows))
	for i, row := range rows {
		before[i] = cloneAnyMap(row.Settings)
	}

	// Simulate the real consumers: they must not mutate the interned map.
	masks := map[string][]byte{"vless": make([]byte, 16)}
	for _, row := range rows {
		if _, err := userread.RuntimeProxySettings(row.Settings, row.Protocol, row.CredentialKey, row.Flow, masks); err != nil {
			t.Fatalf("RuntimeProxySettings failed: %v", err)
		}
	}

	after, err := repo.RuntimeUsersForProtocols(ctx, []string{"vless"})
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range after {
		if !reflect.DeepEqual(before[i], row.Settings) {
			t.Fatalf("settings for row %d mutated by the read path: before %#v after %#v", i, before[i], row.Settings)
		}
	}
	// The underlying stored settings must still be the untouched payload.
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT settings FROM proxies WHERE user_id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "{}" {
		t.Fatalf("stored settings mutated: %q", stored)
	}
}

// tableExists caches positive lookups so the config hot path does not re-query
// the schema on every sync. The cache must never produce a false positive: a
// missing table stays missing, and a table that appears later is seen once.
func TestTableExistsCacheDoesNotProduceFalsePositives(t *testing.T) {
	ctx := context.Background()
	db := openRepositoryHotPathDB(t, `
CREATE TABLE vpn_user_sessions (id INTEGER PRIMARY KEY, user_id INTEGER, client_ip TEXT, ended_at DATETIME);
`)
	repo := NewRepository(db, "sqlite")

	missing, err := repo.tableExists(ctx, "does_not_exist")
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("expected missing table to report false")
	}
	// A repeated lookup of a missing table must still report false.
	missing, err = repo.tableExists(ctx, "does_not_exist")
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("expected cached-miss lookup to still report false")
	}

	present, err := repo.tableExists(ctx, "vpn_user_sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("expected vpn_user_sessions to exist")
	}
	// Second lookup hits the positive cache and must agree.
	present, err = repo.tableExists(ctx, "vpn_user_sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("expected cached-positive lookup to report true")
	}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
