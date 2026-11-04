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
