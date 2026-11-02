package bot

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTelegramUpdateCursorSurvivesRestartAndTokenChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot-cursor.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if _, err := db.Exec(`CREATE TABLE telegram_bot_cursor (token_hash VARCHAR(64) PRIMARY KEY, next_update_id BIGINT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := newStateStore(db)
	if err := store.saveUpdateOffset(ctx, "first-token", 42); err != nil {
		t.Fatal(err)
	}
	if err := store.saveUpdateOffset(ctx, "first-token", 20); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	store = newStateStore(db)
	if got, err := store.updateOffset(ctx, "first-token"); err != nil || got != 42 {
		t.Fatalf("restarted offset = %d, %v; want 42", got, err)
	}
	if got, err := store.updateOffset(ctx, "other-token"); err != nil || got != 0 {
		t.Fatalf("other bot offset = %d, %v; want 0", got, err)
	}
	var storedKey string
	if err := db.QueryRow(`SELECT token_hash FROM telegram_bot_cursor`).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey == "first-token" || len(storedKey) != 64 {
		t.Fatalf("token leaked into cursor key: %q", storedKey)
	}
}
