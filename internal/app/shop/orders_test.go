package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOrderSnapshotsPriceAndRetriesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orders.db")
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
		`CREATE TABLE shop_wallet_accounts (telegram_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE shop_wallet_entries (reference VARCHAR(128) PRIMARY KEY, telegram_id BIGINT NOT NULL, delta BIGINT NOT NULL, reason VARCHAR(32) NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE shop_products (id INTEGER PRIMARY KEY, service_id BIGINT NOT NULL, title TEXT NOT NULL, price_toman BIGINT NOT NULL, data_limit_bytes BIGINT NOT NULL, duration_days INTEGER NOT NULL, enabled INTEGER NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE shop_orders (id VARCHAR(36) PRIMARY KEY, request_key VARCHAR(128) NOT NULL UNIQUE, telegram_id BIGINT NOT NULL, product_id BIGINT NOT NULL, service_id BIGINT NOT NULL, price_toman BIGINT NOT NULL, data_limit_bytes BIGINT NOT NULL, duration_days INTEGER NOT NULL, status VARCHAR(32) NOT NULL, username VARCHAR(34), created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`INSERT INTO shop_products VALUES (1, 42, 'Basic', 80000, 1073741824, 30, 1, '2026-01-01')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store := Orders{DB: db}
	ctx := context.Background()
	first, err := store.Place(ctx, 123, 1, "callback-1")
	if err != nil || first.PriceToman != 80000 || first.ServiceID != 42 {
		t.Fatalf("first order = %+v, %v", first, err)
	}
	if _, err := db.Exec(`UPDATE shop_products SET price_toman = 90000, service_id = 43 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	store.DB = db
	retry, err := store.Place(ctx, 123, 1, "callback-1")
	if err != nil || retry.ID != first.ID || retry.PriceToman != 80000 || retry.ServiceID != 42 {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if _, err := store.Place(ctx, 456, 1, "callback-1"); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("cross-customer reuse error = %v", err)
	}
	wallet := Wallet{DB: db, Dialect: "sqlite"}
	if _, err := wallet.Credit(ctx, 123, 80000, "payment-1"); err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash between wallet debit and order status update.
	if _, err := wallet.Debit(ctx, 123, 80000, "shop-order:"+first.ID); err != nil {
		t.Fatal(err)
	}
	paid, err := store.Pay(ctx, 123, first.ID, wallet)
	if err != nil || paid.Status != "paid" {
		t.Fatalf("recovered payment = %+v, %v", paid, err)
	}
	if _, err := store.Pay(ctx, 123, first.ID, wallet); err != nil {
		t.Fatalf("duplicate payment: %v", err)
	}
	if balance, err := wallet.Balance(ctx, 123); err != nil || balance != 0 {
		t.Fatalf("balance after recovery = %d, %v", balance, err)
	}
	if _, err := store.Pay(ctx, 456, first.ID, wallet); !errors.Is(err, ErrOrderUnavailable) {
		t.Fatalf("cross-customer payment error = %v", err)
	}
	if _, err := db.Exec(`UPDATE shop_products SET enabled = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Place(ctx, 123, 1, "callback-2"); !errors.Is(err, ErrProductUnavailable) {
		t.Fatalf("disabled product error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_orders`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("order count = %d, %v", count, err)
	}
}
