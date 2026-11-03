package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

type fakeProvisioner struct {
	accounts map[string]Account
	creates  int
	fail     error
}

func (f *fakeProvisioner) Lookup(_ context.Context, username string) (Account, bool, error) {
	account, ok := f.accounts[username]
	return account, ok, nil
}

func (f *fakeProvisioner) Create(_ context.Context, account Account) error {
	f.creates++
	if f.fail != nil {
		return f.fail
	}
	f.accounts[account.Username] = account
	return nil
}

func TestProvisionRecoversAfterCreateBeforeCompletion(t *testing.T) {
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
		`CREATE TABLE shop_orders (id TEXT PRIMARY KEY, telegram_id BIGINT, service_id BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, updated_at TEXT)`,
		`INSERT INTO shop_orders VALUES ('11111111-2222-3333-4444-555555555555', 123, 42, 1073741824, 30, 'paid', NULL, '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Orders{DB: db}
	creator := &fakeProvisioner{accounts: map[string]Account{}}
	username, err := store.Provision(ctx, "11111111-2222-3333-4444-555555555555", creator)
	if err != nil || creator.creates != 1 || username == "" {
		t.Fatalf("provision: username=%q creates=%d err=%v", username, creator.creates, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	store.DB = db
	defer db.Close()
	username2, err := store.Provision(ctx, "11111111-2222-3333-4444-555555555555", creator)
	if err != nil || username2 != username || creator.creates != 1 {
		t.Fatalf("repeat provision: username=%q creates=%d err=%v", username2, creator.creates, err)
	}
	if _, err := db.Exec(`UPDATE shop_orders SET status = 'provisioning' WHERE id = '11111111-2222-3333-4444-555555555555'`); err != nil {
		t.Fatal(err)
	}
	creator.accounts[username] = Account{Username: username, TelegramID: 456, ServiceID: 42, DataLimitBytes: 1073741824, HoldDurationSec: 30 * 86400, OrderMarker: "shop-order:11111111-2222-3333-4444-555555555555"}
	if _, err := store.Provision(ctx, "11111111-2222-3333-4444-555555555555", creator); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("mismatched account accepted: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM shop_orders`).Scan(&status); err != nil || status != "provisioning" {
		t.Fatalf("unsafe status %q, %v", status, err)
	}
}

func TestPurchaseRoundTripWithCrashAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "purchase.db")
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
		`CREATE TABLE shop_wallet_entries (reference TEXT PRIMARY KEY, telegram_id BIGINT, delta BIGINT, reason TEXT, created_at TEXT)`,
		`CREATE TABLE shop_products (id INTEGER PRIMARY KEY, service_id BIGINT, title TEXT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER, created_at TEXT)`,
		`CREATE TABLE shop_orders (id TEXT PRIMARY KEY, request_key TEXT UNIQUE, telegram_id BIGINT, product_id BIGINT, service_id BIGINT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, created_at TEXT, updated_at TEXT)`,
		`INSERT INTO shop_products VALUES (1, 42, 'Basic', 80000, 1073741824, 30, 1, '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Orders{DB: db}
	wallet := Wallet{DB: db, Dialect: "sqlite"}
	if _, err := wallet.Credit(ctx, 123, 80000, "bank-verified-1"); err != nil {
		t.Fatal(err)
	}
	order, err := store.Place(ctx, 123, 1, "telegram-click-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	store.DB, wallet.DB = db, db
	defer db.Close()
	paid, err := store.Pay(ctx, 123, order.ID, wallet)
	if err != nil || paid.Status != "paid" {
		t.Fatalf("pay after restart = %+v, %v", paid, err)
	}
	creator := &fakeProvisioner{accounts: map[string]Account{}, fail: errors.New("node delivery temporarily unavailable")}
	if _, err := store.Provision(ctx, order.ID, creator); err == nil {
		t.Fatal("failed provisioning reported success")
	}
	creator.fail = nil
	unfulfilled, err := store.Unfulfilled(ctx, 25)
	if err != nil || len(unfulfilled) != 1 || unfulfilled[0] != order.ID {
		t.Fatalf("recovery queue = %v, %v", unfulfilled, err)
	}
	// The background recovery path invokes provisioning directly. It must not
	// invoke Pay again or mutate the wallet a second time.
	username, err := store.Provision(ctx, unfulfilled[0], creator)
	if err != nil || username == "" {
		t.Fatalf("recovered provision = %q, %v", username, err)
	}
	if balance, err := wallet.Balance(ctx, 123); err != nil || balance != 0 {
		t.Fatalf("wallet after retry = %d, %v", balance, err)
	}
	var debitCount, accountCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries WHERE delta < 0`).Scan(&debitCount); err != nil {
		t.Fatal(err)
	}
	accountCount = len(creator.accounts)
	if debitCount != 1 || accountCount != 1 {
		t.Fatalf("duplicate financial or account effect: debits=%d accounts=%d", debitCount, accountCount)
	}
	if unfulfilled, err := store.Unfulfilled(ctx, 25); err != nil || len(unfulfilled) != 0 {
		t.Fatalf("completed order still in recovery queue = %v, %v", unfulfilled, err)
	}
	orders, err := store.ListCustomerOrders(ctx, 123)
	if err != nil || len(orders) != 1 || orders[0].Status != "completed" || orders[0].Username == nil || *orders[0].Username != username {
		t.Fatalf("order after retry = %+v, %v", orders, err)
	}
}
