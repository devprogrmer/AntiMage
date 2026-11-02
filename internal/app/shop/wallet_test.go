package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestWalletIdempotencyAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
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
		`CREATE TABLE shop_wallet_accounts (telegram_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0))`,
		`CREATE TABLE shop_wallet_entries (reference VARCHAR(128) PRIMARY KEY, telegram_id BIGINT NOT NULL, delta BIGINT NOT NULL CHECK (delta != 0), reason VARCHAR(32) NOT NULL, created_at DATETIME NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	w := Wallet{DB: db, Dialect: "sqlite"}
	ctx := context.Background()
	if got, err := w.Credit(ctx, 123, 800, "payment-1"); err != nil || got != 800 {
		t.Fatalf("credit = %d, %v", got, err)
	}
	if got, err := w.Credit(ctx, 123, 800, "payment-1"); err != nil || got != 800 {
		t.Fatalf("duplicate credit = %d, %v", got, err)
	}
	if got, err := w.Debit(ctx, 123, 800, "order-1"); err != nil || got != 0 {
		t.Fatalf("debit = %d, %v", got, err)
	}
	if got, err := w.Debit(ctx, 123, 800, "order-1"); err != nil || got != 0 {
		t.Fatalf("duplicate debit = %d, %v", got, err)
	}
	if _, err := w.Debit(ctx, 123, 1, "order-2"); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("overdraw error = %v", err)
	}
	if _, err := w.Credit(ctx, 456, 800, "payment-1"); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("conflicting reference error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	w.DB = db
	if got, err := w.Balance(ctx, 123); err != nil || got != 0 {
		t.Fatalf("balance after restart = %d, %v", got, err)
	}
	if got, err := w.Credit(ctx, 123, 800, "payment-1"); err != nil || got != 0 {
		t.Fatalf("retry after restart = %d, %v", got, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("journal count = %d, %v", count, err)
	}
}
