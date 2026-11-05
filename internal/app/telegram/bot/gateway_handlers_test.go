package bot

import (
	"context"
	"github.com/antimage/antimage/internal/app/shop"
	"strings"
	"testing"
)

type fakeGatewayTopups struct {
	fakeTopups
	gatewayOwner int64
	gatewayCount int
}

func (f *fakeGatewayTopups) GatewayEnabled() bool { return true }
func (f *fakeGatewayTopups) CreateGateway(_ context.Context, owner, amount int64, key string) (shop.GatewayPayment, error) {
	f.gatewayOwner = owner
	f.gatewayCount++
	return shop.GatewayPayment{ID: "11111111-2222-3333-4444-555555555555", AmountToman: amount, CheckoutURL: "https://sandbox.zarinpal.com/pg/StartPay/S00000000000000000001", Status: "awaiting"}, nil
}
func (f *fakeGatewayTopups) CheckGateway(_ context.Context, owner int64, id string) (shop.GatewayPayment, error) {
	f.gatewayOwner = owner
	return shop.GatewayPayment{ID: id, Status: "paid"}, nil
}
func (f *fakeGatewayTopups) ListGateway(context.Context, int64) ([]shop.GatewayPayment, error) {
	return nil, nil
}
func TestGatewayBotRequiresOwnPrivateChatAndLocalizes(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeGatewayTopups{}
	b.topups = f
	for _, chat := range []Chat{{ID: -100, Type: "group"}, {ID: 999, Type: "private"}} {
		b.handleMessage(context.Background(), settings, &Message{MessageID: 1, From: &User{ID: 321}, Chat: chat, Text: "/payonline 1234"})
	}
	if f.gatewayCount != 0 {
		t.Fatal("untrusted chat created checkout")
	}
	for _, lang := range []string{"fa", "en", "ru"} {
		b.handleMessage(context.Background(), settings, &Message{MessageID: 2, From: &User{ID: 321, LanguageCode: lang}, Chat: Chat{ID: 321, Type: "private"}, Text: "/payonline 1234"})
		call, ok := lastCall(*calls, "sendMessage")
		if !ok || !strings.Contains(call.body["text"].(string), "1234") {
			t.Fatalf("%s checkout missing: %+v", lang, *calls)
		}
	}
	if f.gatewayCount != 3 || f.gatewayOwner != 321 {
		t.Fatalf("gateway=%+v", f)
	}
}
