package shop

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func paymentDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payment.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE shop_wallet_accounts (telegram_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL)`,
		`CREATE TABLE shop_wallet_entries (reference TEXT PRIMARY KEY, telegram_id BIGINT NOT NULL, delta BIGINT NOT NULL, reason TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE shop_orders (id TEXT PRIMARY KEY, request_key TEXT, telegram_id BIGINT, product_id BIGINT, service_id BIGINT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, updated_at TEXT)`,
		`INSERT INTO shop_wallet_accounts VALUES (123, 100000)`,
		`INSERT INTO shop_orders VALUES ('order-1', 'request-1', 123, 1, 42, 80000, 1073741824, 30, 'pending', NULL, '')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func TestPaymentFailureRollsBackDebitAndSurvivesRestart(t *testing.T) {
	for _, boundary := range []string{"journal", "order"} {
		t.Run(boundary, func(t *testing.T) {
			db, path := paymentDB(t)
			trigger := `CREATE TRIGGER fail_payment BEFORE INSERT ON shop_wallet_entries BEGIN SELECT RAISE(ABORT, 'injected journal failure'); END`
			if boundary == "order" {
				trigger = `CREATE TRIGGER fail_payment BEFORE UPDATE OF status ON shop_orders WHEN NEW.status = 'paid' BEGIN SELECT RAISE(ABORT, 'injected order failure'); END`
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := (Orders{DB: db}).Pay(ctx, 123, "order-1", Wallet{DB: db, Dialect: "sqlite"}); err == nil {
				t.Fatal("injected failure was ignored")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.SetMaxOpenConns(1)
			wallet := Wallet{DB: reopened, Dialect: "sqlite"}
			if balance, err := wallet.Balance(ctx, 123); err != nil || balance != 100000 {
				t.Fatalf("rollback balance=%d err=%v", balance, err)
			}
			var status string
			var count int
			if err := reopened.QueryRow(`SELECT status FROM shop_orders`).Scan(&status); err != nil || status != "pending" {
				t.Fatalf("rollback status=%s err=%v", status, err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rollback journal count=%d err=%v", count, err)
			}
			if _, err := reopened.Exec(`DROP TRIGGER fail_payment`); err != nil {
				t.Fatal(err)
			}
			if order, err := (Orders{DB: reopened}).Pay(ctx, 123, "order-1", wallet); err != nil || order.Status != "paid" {
				t.Fatalf("retry=%+v err=%v", order, err)
			}
			if balance, err := wallet.Balance(ctx, 123); err != nil || balance != 20000 {
				t.Fatalf("retry balance=%d err=%v", balance, err)
			}
		})
	}
}

func TestConcurrentPaymentDebitsOnce(t *testing.T) {
	db, _ := paymentDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := (Orders{DB: db}).Pay(context.Background(), 123, "order-1", Wallet{DB: db, Dialect: "sqlite"})
			if err != nil || order.Status != "paid" {
				t.Errorf("concurrent payment=%+v err=%v", order, err)
			}
		}()
	}
	wg.Wait()
	var balance, count int64
	if err := db.QueryRow(`SELECT balance FROM shop_wallet_accounts`).Scan(&balance); err != nil || balance != 20000 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("journal count=%d err=%v", count, err)
	}
}

func TestConcurrentOrdersCannotOverspendWallet(t *testing.T) {
	db, _ := paymentDB(t)
	if _, err := db.Exec(`INSERT INTO shop_orders SELECT 'order-2', 'request-2', telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username, updated_at FROM shop_orders WHERE id = 'order-1'`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, id := range []string{"order-1", "order-2"} {
		go func(id string) {
			_, err := (Orders{DB: db}).Pay(context.Background(), 123, id, Wallet{DB: db, Dialect: "sqlite"})
			results <- err
		}(id)
	}
	paid, insufficient := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			paid++
		} else if errors.Is(err, ErrInsufficientFunds) {
			insufficient++
		} else {
			t.Fatal(err)
		}
	}
	if paid != 1 || insufficient != 1 {
		t.Fatalf("paid=%d insufficient=%d", paid, insufficient)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("journal count=%d err=%v", count, err)
	}
	if balance, err := (Wallet{DB: db, Dialect: "sqlite"}).Balance(context.Background(), 123); err != nil || balance != 20000 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
}
