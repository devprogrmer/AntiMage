package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRenderUserNotificationTemplateExpandsSupportedPlaceholders(t *testing.T) {
	got, err := RenderUserNotificationTemplate(
		"Hi {{username}}: {{used_traffic}} / {{data_limit}}, {{status}}",
		UserNotificationContext{Username: "alice", UsedTraffic: "1 GB", DataLimit: "5 GB", Status: "active"},
	)
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	if want := "Hi alice: 1 GB / 5 GB, active"; got != want {
		t.Fatalf("rendered message = %q, want %q", got, want)
	}
}

func TestRenderUserNotificationTemplateRejectsUnknownPlaceholders(t *testing.T) {
	_, err := RenderUserNotificationTemplate("Hello {{unknown}}", UserNotificationContext{Username: "alice"})
	if err == nil {
		t.Fatal("expected unknown placeholder to be rejected")
	}
}

func TestRenderUserNotificationTemplateRejectsEmptyOutput(t *testing.T) {
	_, err := RenderUserNotificationTemplate("   ", UserNotificationContext{})
	if err == nil {
		t.Fatal("expected empty message to be rejected")
	}
}

func TestSendDirectMessageTargetsOnlyTheRequestedUser(t *testing.T) {
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")
	var payload map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	if err := sender.SendDirectMessage(context.Background(), 123456, "Your account is ready"); err != nil {
		t.Fatalf("send direct message: %v", err)
	}
	if payload["chat_id"] != float64(123456) || payload["text"] != "Your account is ready" {
		t.Fatalf("unexpected Telegram request: %#v", payload)
	}
}

func TestSendDirectMessageRejectsNonpositiveRecipient(t *testing.T) {
	_, repo := testTelegramRepo(t)
	if err := NewSender(repo, "").SendDirectMessage(context.Background(), 0, "Hi"); err == nil {
		t.Fatal("expected invalid recipient to be rejected")
	}
}
