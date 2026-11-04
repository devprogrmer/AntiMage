package bot

import (
	"context"
	"strings"
	"testing"
)

func TestAdminNodeAndSalesUseSelectedLanguage(t *testing.T) {
	for _, tc := range []struct {
		language string
		nodes    string
		sales    string
	}{
		{"en", "AntiMage nodes", "AntiMage sales"},
		{"fa", "نودهای AntiMage", "فروش AntiMage"},
		{"ru", "Узлы AntiMage", "Продажи AntiMage"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			b, calls, settings := newTestBot(t, newFakeUsers())
			b.nodes = &fakeNodes{}
			b.shop = &fakeShop{}
			ctx := context.WithValue(context.Background(), languageContextKey{}, tc.language)
			b.handleAdminNodes(ctx, settings, 100, "/nodes", "")
			call, ok := lastCall(*calls, "sendMessage")
			if !ok || !strings.Contains(call.body["text"].(string), tc.nodes) {
				t.Fatalf("node language %s: %+v", tc.language, *calls)
			}
			b.handleAdminSales(ctx, settings, 100)
			call, ok = lastCall(*calls, "sendMessage")
			if !ok || !strings.Contains(call.body["text"].(string), tc.sales) {
				t.Fatalf("sales language %s: %+v", tc.language, *calls)
			}
		})
	}
}

func TestUserAndSystemViewsUseSelectedLanguage(t *testing.T) {
	user := UserView{Username: "demo", Status: "active"}
	info := SystemInfo{Version: "test"}
	for _, tc := range []struct {
		language string
		user     string
		system   string
		limit    string
	}{
		{"en", "Username", "Total users", "Unlimited"},
		{"fa", "نام کاربری", "کل کاربران", "نامحدود"},
		{"ru", "Пользователь", "Всего пользователей", "Без ограничений"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), languageContextKey{}, tc.language)
			for _, view := range []string{userDetailText(ctx, user), userUsageText(ctx, user)} {
				if !strings.Contains(view, tc.user) || !strings.Contains(view, tc.limit) {
					t.Fatalf("user view is not localized: %s", view)
				}
			}
			if view := systemInfoText(ctx, info); !strings.Contains(view, tc.system) {
				t.Fatalf("system view is not localized: %s", view)
			}
		})
	}
}
