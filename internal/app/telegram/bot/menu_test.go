package bot

import (
	"context"
	"strings"
	"testing"
)

func TestKeyboardTranslationPrefersCompleteLabels(t *testing.T) {
	for _, tc := range []struct {
		language string
		want     string
	}{
		{language: "fa", want: "بله، حذف شود"},
		{language: "ru", want: "Да, удалить"},
	} {
		for i := 0; i < 100; i++ {
			keyboard := localizeKeyboard(tc.language, confirmDeleteKeyboard("alice"))
			got := keyboard.InlineKeyboard[0][0].Text
			if got != "✅ "+tc.want || strings.Contains(got, "Yes") {
				t.Fatalf("%s confirmation label = %q", tc.language, got)
			}
		}
	}
}

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

func TestMiniAppButtonRequiresHTTPS(t *testing.T) {
	for _, raw := range []string{"http://panel.example/telegram-app", "https://user:pass@panel.example/telegram-app", "https://panel.example/telegram-app#fragment"} {
		menu := appendMiniAppButton(customerMenuKeyboard(), raw)
		if len(menu.InlineKeyboard) != 5 {
			t.Fatalf("unsafe URL produced Mini App button: %q", raw)
		}
	}
	menu := appendMiniAppButton(customerMenuKeyboard(), "https://panel.example/telegram-app")
	last := menu.InlineKeyboard[len(menu.InlineKeyboard)-1]
	if len(last) != 1 || last[0].WebApp == nil || last[0].WebApp.URL != "https://panel.example/telegram-app" || last[0].CallbackData != "" {
		t.Fatalf("Mini App button: %+v", last)
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
