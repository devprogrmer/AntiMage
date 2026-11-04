package shop

import (
	"context"
	"errors"
	platformdb "github.com/antimage/antimage/internal/platform/db"
	"github.com/google/uuid"
	"os"
	"sync"
	"testing"
	"time"
)

func TestShopCommerceExternalMySQL(t *testing.T) {
	if os.Getenv("ANTIMAGE_TEST_COMMERCE_DB") != "1" {
		t.Skip("requires an explicitly provisioned, migrated test database")
	}
	pool, err := platformdb.Open(os.Getenv("ANTIMAGE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if pool.Dialect != "mysql" {
		t.Skip("external MySQL/MariaDB only")
	}
	db := pool.DB
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	owner := time.Now().UnixNano()
	result, err := db.ExecContext(ctx, `INSERT INTO shop_products(service_id,title,price_toman,data_limit_bytes,duration_days,enabled,created_at,request_key) VALUES(42,'Commerce integration',80000,100,30,1,?,?)`, time.Now().UTC().Format("2006-01-02 15:04:05"), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	product, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, q := range []string{`DELETE FROM shop_gateway_payments WHERE telegram_id=?`, `DELETE FROM shop_orders WHERE telegram_id=?`, `DELETE FROM shop_wallet_entries WHERE telegram_id=?`, `DELETE FROM shop_wallet_accounts WHERE telegram_id=?`} {
			if _, err := db.ExecContext(cleanup, q, owner); err != nil {
				t.Error(err)
			}
		}
		if _, err := db.ExecContext(cleanup, `DELETE FROM shop_products WHERE id=?`, product); err != nil {
			t.Error(err)
		}
	})
	wallet := Wallet{DB: db, Dialect: "mysql"}
	orders := Orders{DB: db}
	if _, err := wallet.Credit(ctx, owner, 100000, "mysql-credit:"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	order, err := orders.Place(ctx, owner, product, "mysql-order:"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := orders.Pay(ctx, owner, order.ID, wallet); err != nil || p.Status != "paid" {
				t.Errorf("concurrent pay=%+v %v", p, err)
			}
		}()
	}
	wg.Wait()
	if balance, err := wallet.Balance(ctx, owner); err != nil || balance != 20000 {
		t.Fatalf("balance=%d %v", balance, err)
	}
	other, err := orders.Place(ctx, owner, product, "mysql-order:"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orders.Pay(ctx, owner, other.ID, wallet); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("overspend=%v", err)
	}
	g := &fakeGateway{reference: uuid.NewString()}
	payments := GatewayPayments{DB: db, Dialect: "mysql", Factory: func(string, bool) PaymentGateway { return g }}
	p, err := payments.Create(ctx, owner, 1234, "mysql-gateway:"+uuid.NewString(), "11111111-2222-3333-4444-555555555555", true, "https://example.com/telegram-payment-return")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := payments.Verify(ctx, p.ID, "S000000000000000000000000000000001"); err != nil {
			t.Fatal(err)
		}
	}
	if balance, err := wallet.Balance(ctx, owner); err != nil || balance != 21234 {
		t.Fatalf("gateway balance=%d %v", balance, err)
	}
}
