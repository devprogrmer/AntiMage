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

func TestAdminCommandsCannotRunFromAllowedNotificationGroup(t *testing.T) {
	users := newFakeUsers()
	users.users["alice"] = UserView{Username: "alice", Status: "active"}
	b, calls, settings := newTestBot(t, users)
	settings.AdminChatIDs = append(settings.AdminChatIDs, -100)
	for _, sender := range []int64{100, 999} {
		b.handleMessage(context.Background(), settings, &Message{
			From: &User{ID: sender}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/user alice",
		})
	}
	b.handleCallback(context.Background(), settings, &CallbackQuery{
		ID: "group-action", From: &User{ID: 100}, Message: &Message{Chat: Chat{ID: -100, Type: "supergroup"}}, Data: cbDeleteYes + "alice",
	})
	if len(users.deleted) != 0 {
		t.Fatal("group callback deleted a user")
	}
	for _, call := range *calls {
		if call.method == "sendMessage" || call.method == "editMessageText" {
			t.Fatalf("notification group received admin response: %+v", *calls)
		}
	}
	if len(*calls) != 1 || (*calls)[0].method != "answerCallbackQuery" {
		t.Fatalf("notification group received admin response: %+v", *calls)
	}
}
