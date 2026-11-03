package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/app/shop"
)

type fakeShop struct {
	owner       int64
	placeCount  int
	payCount    int
	creditCount int
	requestKey  string
	payErr      error
}

func (f *fakeShop) SalesOverview(context.Context) (shop.SalesOverview, error) {
	return shop.SalesOverview{TotalOrders: 2, UnfulfilledOrders: 1, CompletedOrders: 1, PaidToman: 160000}, nil
}

func (f *fakeShop) Balance(_ context.Context, owner int64) (int64, error) {
	f.owner = owner
	return 100000, nil
}
func (f *fakeShop) History(_ context.Context, owner int64) ([]shop.WalletEntry, error) {
	f.owner = owner
	return []shop.WalletEntry{{Reference: "topup:1", Delta: 100000, Reason: "topup", CreatedAt: "2026-11-03 10:00:00"}}, nil
}
func (f *fakeShop) Products(context.Context) ([]shop.Product, error) {
	return []shop.Product{{ID: 5, Title: "Basic", PriceToman: 80000, DataLimitBytes: 1 << 30, DurationDays: 30}}, nil
}
func (f *fakeShop) Orders(_ context.Context, owner int64) ([]shop.Order, error) {
	f.owner = owner
	return nil, nil
}
func (f *fakeShop) Place(_ context.Context, owner, productID int64, requestKey string) (shop.Order, error) {
	f.owner = owner
	f.placeCount++
	f.requestKey = requestKey
	return shop.Order{ID: "11111111-2222-3333-4444-555555555555", PriceToman: 80000}, nil
}
func (f *fakeShop) PayAndProvision(_ context.Context, owner int64, _ string) (string, error) {
	f.owner = owner
	f.payCount++
	return "tg111111112222333344445555555555", f.payErr
}
func (f *fakeShop) AdminCredit(_ context.Context, owner, amount int64, _ string) (int64, error) {
	f.owner = owner
	f.creditCount++
	return amount, nil
}
func (f *fakeShop) AddProduct(_ context.Context, product shop.Product, _ string) (shop.Product, error) {
	product.ID = 6
	return product, nil
}
func (f *fakeShop) DisableProduct(context.Context, int64) error { return nil }

func TestShopCallbacksRequireOwnPrivateChat(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeShop{}
	b.shop = f
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/shop"})
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "Basic") {
		t.Fatalf("private catalog not shown: %+v", *calls)
	}
	for _, chat := range []Chat{{ID: -100, Type: "supergroup"}, {ID: 999, Type: "private"}} {
		b.handleCallback(ctx, settings, &CallbackQuery{ID: "wrong", From: &User{ID: 321}, Message: &Message{Chat: chat}, Data: "shop:buy:5"})
	}
	if f.placeCount != 0 {
		t.Fatal("unauthorized chat created an order")
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "click-1", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "shop:buy:5"})
	if f.placeCount != 1 || f.owner != 321 || !strings.HasPrefix(f.requestKey, "tg:") {
		t.Fatalf("order callback: %+v", f)
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "click-2", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "shop:pay:11111111-2222-3333-4444-555555555555"})
	if f.payCount != 1 || f.owner != 321 {
		t.Fatalf("payment callback: %+v", f)
	}
}

func TestShopInsufficientBalanceDoesNotClaimDelivery(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	b.shop = &fakeShop{payErr: shop.ErrInsufficientFunds}
	b.handleCallback(context.Background(), settings, &CallbackQuery{ID: "click", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "shop:pay:11111111-2222-3333-4444-555555555555"})
	call, ok := lastCall(*calls, "sendMessage")
	if !ok || !strings.Contains(call.body["text"].(string), "insufficient") {
		t.Fatalf("insufficient balance text missing: %+v", *calls)
	}
}

func TestAdminCreditOnlyFromAllowlistedPrivateAccount(t *testing.T) {
	b, _, settings := newTestBot(t, newFakeUsers())
	f := &fakeShop{}
	b.shop = f
	for _, msg := range []Message{
		{From: &User{ID: 100}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/credit 321 1000 payment-1"},
		{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/credit 321 1000 payment-1"},
		{From: &User{ID: 100}, Chat: Chat{ID: 999, Type: "private"}, Text: "/credit 321 1000 payment-1"},
	} {
		b.handleMessage(context.Background(), settings, &msg)
	}
	if f.creditCount != 0 {
		t.Fatalf("unauthorized wallet credits: %d", f.creditCount)
	}
	b.handleMessage(context.Background(), settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/credit 321 1000 payment-1"})
	if f.creditCount != 1 || f.owner != 321 {
		t.Fatalf("authorized wallet credit failed: %+v", f)
	}
}

func TestSalesOverviewOnlyForAdmin(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	b.shop = &fakeShop{}
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/sales"})
	if len(*calls) != 0 {
		t.Fatal("customer received sales report")
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/sales"})
	call, ok := lastCall(*calls, "sendMessage")
	if !ok || !strings.Contains(call.body["text"].(string), "160000") {
		t.Fatalf("sales report missing: %+v", *calls)
	}
}

func TestWalletHistoryOnlyOwnerOrPrivateAdmin(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeShop{}
	b.shop = f
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/transactions"})
	if f.owner != 321 {
		t.Fatalf("customer saw wallet %d", f.owner)
	}
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "topup:1") {
		t.Fatalf("history response: %+v", *calls)
	}
	f.owner = 0
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/wallet 999"})
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/wallet 999"})
	if f.owner != 0 {
		t.Fatalf("unauthorized wallet audit: %d", f.owner)
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/wallet 999"})
	if f.owner != 999 {
		t.Fatalf("admin wallet audit used owner %d", f.owner)
	}
}
