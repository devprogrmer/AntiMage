package support

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTicketOwnershipIdempotencyAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	db := open()
	for _, statement := range []string{
		`CREATE TABLE telegram_support_tickets (id TEXT PRIMARY KEY, telegram_id BIGINT, status TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE telegram_support_messages (id TEXT PRIMARY KEY, ticket_id TEXT, author TEXT, content TEXT, request_key TEXT UNIQUE, created_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Store{DB: db}
	first, err := store.Create(ctx, 123, "Cannot connect", "customer-message-1")
	if err != nil || first.ID == "" {
		t.Fatalf("create = %+v, %v", first, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	store.DB = db
	retry, err := store.Create(ctx, 123, "Cannot connect", "customer-message-1")
	if err != nil || retry.ID != first.ID {
		t.Fatalf("restart retry = %+v, %v", retry, err)
	}
	if _, err := store.Create(ctx, 456, "Cannot connect", "customer-message-1"); !errors.Is(err, ErrMessageConflict) {
		t.Fatalf("cross-user reference accepted: %v", err)
	}
	if _, err := store.Get(ctx, first.ID, 456, false); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("cross-user ticket read: %v", err)
	}
	replied, err := store.Reply(ctx, first.ID, "Check your node", "admin-message-1")
	if err != nil || len(replied.Messages) != 2 {
		t.Fatalf("reply = %+v, %v", replied, err)
	}
	if again, err := store.Reply(ctx, first.ID, "Check your node", "admin-message-1"); err != nil || len(again.Messages) != 2 {
		t.Fatalf("duplicate reply = %+v, %v", again, err)
	}
	if _, err := store.Reply(ctx, first.ID, "Different", "admin-message-1"); !errors.Is(err, ErrMessageConflict) {
		t.Fatalf("changed reply accepted: %v", err)
	}
	if original, err := store.Create(ctx, 123, "Cannot connect", "customer-message-1"); err != nil || len(original.Messages) != 2 {
		t.Fatalf("create retry after reply = %+v, %v", original, err)
	}
	own, err := store.List(ctx, 123, false)
	if err != nil || len(own) != 1 {
		t.Fatalf("own list = %+v, %v", own, err)
	}
	other, err := store.List(ctx, 456, false)
	if err != nil || len(other) != 0 {
		t.Fatalf("other list = %+v, %v", other, err)
	}
}
