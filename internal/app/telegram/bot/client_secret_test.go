package bot

import (
	"context"
	"strings"
	"testing"
)

func TestTelegramTransportErrorRedactsBotToken(t *testing.T) {
	const secret = "123456:very-secret-bot-token"
	c := newClient("http://127.0.0.1:1")
	_, err := c.getUpdates(context.Background(), Settings{Token: secret}, 0, 1)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("bot token leaked in error: %s", err)
	}
}
