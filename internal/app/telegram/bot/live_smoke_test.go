package bot

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in test uses only a private test bot and the specified owner's /start.
// Credentials are read from a local file and never logged or stored by the test.
func TestLiveTelegramStart(t *testing.T) {
	path := os.Getenv("ANTIMAGE_LIVE_TEST_CONFIG")
	if path == "" {
		t.Skip("live Telegram test not configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read live test configuration")
	}
	var config struct {
		Token  string `json:"token"`
		ChatID int64  `json:"chat_id"`
	}
	if err := json.Unmarshal(raw, &config); err != nil || strings.TrimSpace(config.Token) == "" || config.ChatID <= 0 {
		t.Fatal("invalid live test configuration")
	}
	settings := Settings{Enabled: true, Token: config.Token, ProxyURL: os.Getenv("ANTIMAGE_LIVE_TEST_PROXY"), AdminChatIDs: []int64{config.ChatID}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b := New(Options{Authorizer: fakeAuthorizer{ok: true, allowedID: config.ChatID}, Users: newFakeUsers(), System: fakeSystem{}, Logf: func(format string, args ...any) { t.Errorf("bot delivery failed") }})
	updates, err := b.client.getUpdates(ctx, settings, 0, 0)
	if err != nil {
		t.Fatalf("could not receive updates from the test bot: %s", redactToken(err.Error(), config.Token))
	}
	for _, update := range updates {
		msg := update.Message
		if msg == nil || msg.From == nil || msg.From.ID != config.ChatID || msg.Chat.ID != config.ChatID || msg.Chat.Type != "private" || msg.Text != "/start" {
			continue
		}
		b.handleMessage(ctx, settings, msg)
		t.Log("processed the owner's real /start update and sent AntiMage menu")
		return
	}
	t.Fatal("no private /start update from the configured owner; send /start to the test bot")
}
