package shop

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func changeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := paymentDB(t)
	for _, q := range []string{
		`ALTER TABLE shop_orders ADD COLUMN created_at TEXT`,
		`CREATE TABLE shop_products (id BIGINT PRIMARY KEY, service_id BIGINT, title TEXT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER)`,
		`INSERT INTO shop_products VALUES (1,42,'Extension',1000,100,30,1)`,
		`CREATE TABLE users (username TEXT PRIMARY KEY, telegram_id BIGINT, service_id BIGINT, status TEXT, data_limit BIGINT)`,
		`INSERT INTO users VALUES ('alice',123,42,'active',100)`,
		`INSERT INTO users VALUES ('other',456,42,'active',100)`,
		`CREATE TABLE shop_order_changes (order_id TEXT PRIMARY KEY, kind TEXT, username TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestChangeOwnershipReplayAndAtomicFulfillment(t *testing.T) {
	db := changeDB(t)
	store := Orders{DB: db}
	ctx := context.Background()
	if _, err := store.PlaceChange(ctx, 123, 1, "other", "renew", "stolen"); !errors.Is(err, ErrOrderUnavailable) {
		t.Fatalf("cross owner=%v", err)
	}
	order, err := store.PlaceChange(ctx, 123, 1, "alice", "renew", "renew-1")
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := store.PlaceChange(ctx, 123, 1, "alice", "renew", "renew-1"); err != nil || retry.ID != order.ID {
		t.Fatalf("replay=%+v %v", retry, err)
	}
	if _, err := store.PlaceChange(ctx, 123, 1, "alice", "volume", "renew-1"); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("changed intent=%v", err)
	}
	apply := func(ctx context.Context, tx *sql.Tx, order Order, change OrderChange) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET data_limit=data_limit+? WHERE username=?`, order.DataLimitBytes, change.Username)
		return err
	}
	if _, err := store.FulfillChange(ctx, order.ID, apply); !errors.Is(err, ErrOrderUnavailable) {
		t.Fatalf("unpaid delivery=%v", err)
	}
	if _, err := store.Pay(ctx, 123, order.ID, Wallet{DB: db, Dialect: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF status ON shop_orders WHEN NEW.status='completed' BEGIN SELECT RAISE(ABORT,'completion failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FulfillChange(ctx, order.ID, apply); err == nil {
		t.Fatal("failure was ignored")
	}
	var limit int64
	if err := db.QueryRow(`SELECT data_limit FROM users WHERE username='alice'`).Scan(&limit); err != nil || limit != 100 {
		t.Fatalf("failed delivery changed account=%d %v", limit, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_completion`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if name, err := store.FulfillChange(ctx, order.ID, apply); err != nil || name != "alice" {
			t.Fatalf("fulfill=%s %v", name, err)
		}
	}
	if err := db.QueryRow(`SELECT data_limit FROM users WHERE username='alice'`).Scan(&limit); err != nil || limit != 200 {
		t.Fatalf("repeated delivery changed account=%d %v", limit, err)
	}
	if balance, err := (Wallet{DB: db, Dialect: "sqlite"}).Balance(ctx, 123); err != nil || balance != 99000 {
		t.Fatalf("balance=%d %v", balance, err)
	}
}
