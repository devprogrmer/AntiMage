package bot

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/app/shop"
	_ "modernc.org/sqlite"
)

type testAccountProvisioner struct{ accounts map[string]shop.Account }

func (p *testAccountProvisioner) Lookup(_ context.Context, username string) (shop.Account, bool, error) {
	account, ok := p.accounts[username]
	return account, ok, nil
}
func (p *testAccountProvisioner) Create(_ context.Context, account shop.Account) error {
	p.accounts[account.Username] = account
	return nil
}

type testShopAdapter struct {
	orders   shop.Orders
	wallet   shop.Wallet
	accounts *testAccountProvisioner
}

func (a testShopAdapter) SalesOverview(ctx context.Context) (shop.SalesOverview, error) {
	return a.orders.Overview(ctx)
}

func (a testShopAdapter) Balance(ctx context.Context, id int64) (int64, error) {
	return a.wallet.Balance(ctx, id)
}
func (a testShopAdapter) History(ctx context.Context, id int64) ([]shop.WalletEntry, error) {
	return a.wallet.History(ctx, id, 15)
}
func (a testShopAdapter) Products(ctx context.Context) ([]shop.Product, error) {
	return a.orders.ListProducts(ctx)
}
func (a testShopAdapter) Orders(ctx context.Context, id int64) ([]shop.Order, error) {
	return a.orders.ListCustomerOrders(ctx, id)
}
func (a testShopAdapter) Place(ctx context.Context, id, product int64, key string) (shop.Order, error) {
	return a.orders.Place(ctx, id, product, key)
}
func (a testShopAdapter) PayAndProvision(ctx context.Context, id int64, orderID string) (string, error) {
	if _, err := a.orders.Pay(ctx, id, orderID, a.wallet); err != nil {
		return "", err
	}
	return a.orders.Provision(ctx, orderID, a.accounts)
}
func (a testShopAdapter) AdminCredit(ctx context.Context, id, amount int64, key string) (int64, error) {
	return a.wallet.Credit(ctx, id, amount, key)
}
func (a testShopAdapter) AddProduct(ctx context.Context, product shop.Product, key string) (shop.Product, error) {
	return a.orders.CreateProduct(ctx, product, key)
}
func (a testShopAdapter) DisableProduct(ctx context.Context, id int64) error {
	return a.orders.DisableProduct(ctx, id)
}

func TestTelegramShopEndToEndWithRestartedWallet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot-shop.db")
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
		`CREATE TABLE services (id BIGINT PRIMARY KEY)`,
		`INSERT INTO services VALUES (42)`,
		`CREATE TABLE shop_wallet_accounts (telegram_id BIGINT PRIMARY KEY, balance BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE shop_wallet_entries (reference TEXT PRIMARY KEY, telegram_id BIGINT, delta BIGINT, reason TEXT, created_at TEXT)`,
		`CREATE TABLE shop_products (id INTEGER PRIMARY KEY AUTOINCREMENT, service_id BIGINT, title TEXT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER, created_at TEXT, request_key TEXT UNIQUE)`,
		`CREATE TABLE shop_orders (id TEXT PRIMARY KEY, request_key TEXT UNIQUE, telegram_id BIGINT, product_id BIGINT, service_id BIGINT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, status TEXT, username TEXT, created_at TEXT, updated_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	adapter := testShopAdapter{orders: shop.Orders{DB: db}, wallet: shop.Wallet{DB: db, Dialect: "sqlite"}, accounts: &testAccountProvisioner{accounts: map[string]shop.Account{}}}
	b, calls, settings := newTestBot(t, newFakeUsers())
	b.shop = adapter
	b.handleMessage(ctx, settings, &Message{MessageID: 1, From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/plan-add 42 80000 1 30 Basic"})
	b.handleMessage(ctx, settings, &Message{MessageID: 2, From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/credit 321 80000 bank-reference-1"})
	b.handleMessage(ctx, settings, &Message{MessageID: 3, From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/shop"})
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "Basic") {
		t.Fatalf("product not shown in Telegram: %+v", *calls)
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "buy-1", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "shop:buy:1"})
	orders, err := adapter.orders.ListCustomerOrders(ctx, 321)
	if err != nil || len(orders) != 1 {
		t.Fatalf("order = %+v, %v", orders, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	adapter.orders.DB, adapter.wallet.DB = db, db
	b.shop = adapter
	callback := &CallbackQuery{ID: "pay-1", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "shop:pay:" + orders[0].ID}
	b.handleCallback(ctx, settings, callback)
	b.handleCallback(ctx, settings, callback)
	orders, err = adapter.orders.ListCustomerOrders(ctx, 321)
	if err != nil || len(orders) != 1 || orders[0].Status != "completed" || orders[0].Username == nil {
		t.Fatalf("delivered order = %+v, %v", orders, err)
	}
	if balance, err := adapter.wallet.Balance(ctx, 321); err != nil || balance != 0 {
		t.Fatalf("balance = %d, %v", balance, err)
	}
	if len(adapter.accounts.accounts) != 1 {
		t.Fatalf("accounts created = %d", len(adapter.accounts.accounts))
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/sales"})
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "80000 toman") {
		t.Fatalf("paid order missing from sales report: %+v", *calls)
	}
	var debits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_wallet_entries WHERE delta < 0`).Scan(&debits); err != nil || debits != 1 {
		t.Fatalf("debits = %d, %v", debits, err)
	}
}
