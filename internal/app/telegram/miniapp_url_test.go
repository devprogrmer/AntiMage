package telegram

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMiniAppURLRequiresPublicHTTPSRoute(t *testing.T) {
	_, repo := testTelegramRepo(t)
	ctx := context.Background()
	for _, candidate := range []string{"http://panel.example/telegram-app", "https://panel.example/other", "https://user:pass@panel.example/telegram-app", "https://panel.example/telegram-app#fragment"} {
		value, _ := json.Marshal(candidate)
		if _, err := repo.UpdateSettings(ctx, map[string]json.RawMessage{"mini_app_url": value}); err == nil {
			t.Fatalf("accepted unsafe Mini App URL %q", candidate)
		}
	}
	value, _ := json.Marshal("https://panel.example/telegram-app")
	settings, err := repo.UpdateSettings(ctx, map[string]json.RawMessage{"mini_app_url": value})
	if err != nil || settings.MiniAppURL == nil || *settings.MiniAppURL != "https://panel.example/telegram-app" {
		t.Fatalf("Mini App URL not saved: %+v, %v", settings.MiniAppURL, err)
	}
}
