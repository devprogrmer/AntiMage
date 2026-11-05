package bot

import (
	"context"
	"github.com/antimage/antimage/internal/app/shop"
	"strings"
	"testing"
)

type fakeChangeShop struct {
	fakeShop
	target, kind string
}

func (f *fakeChangeShop) PlaceChange(ctx context.Context, owner, product int64, username, kind, key string) (shop.Order, error) {
	f.target = username
	f.kind = kind
	return f.Place(ctx, owner, product, key)
}
func (f *fakeChangeShop) Products(context.Context) ([]shop.Product, error) {
	return []shop.Product{{ID: 5, ServiceID: 42, Title: "Extension", PriceToman: 1000, DataLimitBytes: 1 << 30, DurationDays: 30}}, nil
}

func TestExtensionButtonsCreateOwnedOrderWithoutCharging(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeChangeShop{}
	b.shop = f
	service := int64(42)
	limit := int64(1000)
	b.customers = &fakeCustomers{accounts: []UserView{{Username: "alice", Status: "active", ServiceID: &service, DataLimit: &limit}}}
	for _, lang := range []string{"fa", "en", "ru"} {
		query := &CallbackQuery{ID: "list-" + lang, From: &User{ID: 321, LanguageCode: lang}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "sx:list:alice"}
		b.handleCallback(context.Background(), settings, query)
		call, ok := lastCall(*calls, "sendMessage")
		if !ok || !strings.Contains(call.body["text"].(string), "Extension") {
			t.Fatalf("%s extension menu missing: %+v", lang, *calls)
		}
	}
	for _, chat := range []Chat{{ID: -100, Type: "supergroup"}, {ID: 999, Type: "private"}} {
		b.handleCallback(context.Background(), settings, &CallbackQuery{ID: "bad", From: &User{ID: 321}, Message: &Message{Chat: chat}, Data: "sx:r:5:alice"})
	}
	if f.placeCount != 0 {
		t.Fatal("unauthorized chat placed order")
	}
	b.handleCallback(context.Background(), settings, &CallbackQuery{ID: "renew-click", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "sx:r:5:alice"})
	if f.placeCount != 1 || f.payCount != 0 || f.target != "alice" || f.kind != "renew" || f.owner != 321 {
		t.Fatalf("extension=%+v", f)
	}
	b.handleCallback(context.Background(), settings, &CallbackQuery{ID: "forged", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "sx:v:5:other"})
	if f.placeCount != 1 {
		t.Fatal("unowned account accepted")
	}
}
