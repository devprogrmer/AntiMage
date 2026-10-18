package usagereflection

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestRecordUserDeletionTxRequiresCommittedTombstoneAndRetainsPendingRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "retirement.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE user_accounting_tombstones(user_id INTEGER PRIMARY KEY, deleted_at DATETIME NOT NULL)`,
		`CREATE TABLE node_usage_user_queue(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, processed_at DATETIME NULL, retirement_pending INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO node_usage_user_queue(id,user_id,processed_at) VALUES (1,7,NULL),(2,7,CURRENT_TIMESTAMP),(3,8,NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	deletedAt := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordUserDeletionTx(context.Background(), tx, "sqlite", []int64{7, 7, -1}, deletedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_accounting_tombstones WHERE user_id=7`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back deletion left durable tombstone: count=%d err=%v", count, err)
	}

	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordUserDeletionTx(context.Background(), tx, "sqlite", []int64{7, 7, -1}, deletedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var gotTime time.Time
	if err := db.QueryRow(`SELECT deleted_at FROM user_accounting_tombstones WHERE user_id=7`).Scan(&gotTime); err != nil {
		t.Fatal(err)
	}
	if !gotTime.Equal(deletedAt) {
		t.Fatalf("tombstone timestamp=%s want=%s", gotTime, deletedAt)
	}
	var pending, processed, other int
	if err := db.QueryRow(`SELECT retirement_pending FROM node_usage_user_queue WHERE id=1`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT retirement_pending FROM node_usage_user_queue WHERE id=2`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT retirement_pending FROM node_usage_user_queue WHERE id=3`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || processed != 0 || other != 0 {
		t.Fatalf("retirement flags pending=%d processed=%d other-user=%d", pending, processed, other)
	}
}
