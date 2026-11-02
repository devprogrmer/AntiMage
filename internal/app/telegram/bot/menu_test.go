package bot

import (
	"context"
	"testing"
)

func TestCustomerMenuUsesPrivateChatAndOwner(t *testing.T) {
	accounts := &fakeCustomers{accounts: []UserView{{Username: "alice", Status: "active"}}}
	b, calls, settings := newTestBot(t, newFakeUsers())
	b.customers = accounts
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/help"})
	if call, ok := lastCall(*calls, "sendMessage"); !ok || call.body["reply_markup"] == nil {
		t.Fatalf("customer help menu missing: %+v", *calls)
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "menu-1", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: 321, Type: "private"}}, Data: "menu:my"})
	if accounts.id != 321 {
		t.Fatalf("account owner = %d, want 321", accounts.id)
	}
	accounts.id = 0
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "menu-2", From: &User{ID: 321}, Message: &Message{Chat: Chat{ID: -100, Type: "supergroup"}}, Data: "menu:my"})
	if accounts.id != 0 {
		t.Fatal("group callback accessed customer accounts")
	}
}
