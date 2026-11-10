package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAccountingAlertUsesConfiguredToggleAndEscapesNodeName(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")
	var message string
	sender := NewSender(repo, "https://telegram.invalid")
	sender.retryDelays = nil
	sender.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		message = payload.Text
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
	})
	reporter := NewReporter(repo, sender)
	if !reporter.NodeAccountingAlert(ctx, "node <private>", "xray", "pending_ack_overdue") {
		t.Fatal("expected successful delivery")
	}
	if !strings.Contains(message, "node &lt;private&gt;") || !strings.Contains(message, "pending_ack_overdue") || strings.Contains(message, "node <private>") {
		t.Fatalf("unexpected alert message: %q", message)
	}
	message = ""
	if _, err := db.Exec(`UPDATE telegram_settings SET event_toggles = '{"node.accounting":false}' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if reporter.NodeAccountingAlert(ctx, "node", "xray", "checkpoint_overdue") {
		t.Fatal("disabled event must remain eligible if notifications are enabled later")
	}
	if message != "" {
		t.Fatalf("disabled event sent: %q", message)
	}
	if _, err := db.Exec(`UPDATE telegram_settings SET event_toggles = '{"node.accounting":true}' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	sender.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"unavailable"}`)), Header: make(http.Header)}, nil
	})
	if NewReporter(repo, sender).NodeAccountingAlert(ctx, "node", "xray", "checkpoint_overdue") {
		t.Fatal("failed delivery must remain eligible for retry")
	}
}

func TestReporterSendsLegacyUserCreatedWhenEnabled(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")

	var payload map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	reporter := NewReporter(repo, sender)
	limit := int64(1024)
	reporter.UserCreated(ctx, UserReport{
		Username:      "alice",
		Owner:         "owner",
		Actor:         "pouria",
		DataLimit:     &limit,
		ResetStrategy: "month",
		Proxies:       []string{"vless", "vmess"},
	})

	text, _ := payload["text"].(string)
	if payload["chat_id"].(float64) != -1001 || payload["message_thread_id"].(float64) != 42 {
		t.Fatalf("unexpected destination payload: %#v", payload)
	}
	for _, needle := range []string{"<b>#UserCreated</b>", "<b>Username:</b> <code>alice</code>", "<b>Belongs To:</b> <code>owner</code>", "<b>By:</b> <code>#pouria</code>"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("message missing %q:\n%s", needle, text)
		}
	}
}

func TestReporterSkipsDisabledToggle(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, `api_token = 'token', use_telegram = 1, logs_chat_id = -1001, event_toggles = '{"user.created":false}'`)

	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	NewReporter(repo, sender).UserCreated(ctx, UserReport{Username: "alice", Actor: "pouria"})
	if calls != 0 {
		t.Fatalf("expected disabled toggle to skip Telegram, got %d calls", calls)
	}
	assertTelegramColumnEmpty(t, db, "last_error")
}

func TestReporterMissingChatIDDoesNotCrashOrRecordError(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, `api_token = 'token', use_telegram = 1, admin_chat_ids = '[]', logs_chat_id = NULL, event_toggles = '{}'`)

	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	NewReporter(repo, sender).Login(ctx, LoginReport{Username: "alice", Success: false})
	if calls != 0 {
		t.Fatalf("expected missing destination to skip Telegram, got %d calls", calls)
	}
	assertTelegramColumnEmpty(t, db, "last_error")
}

func TestReporterTelegramErrorDoesNotFailMutationPath(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"description":"temporary bad gateway"}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	NewReporter(repo, sender).AdminDeleted(ctx, AdminReport{Username: "oldadmin", Actor: "pouria"})
	assertTelegramColumnContains(t, db, "last_error", "temporary bad gateway")
}

func TestReporterSkipsWhenTelegramDisabled(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, `api_token = 'token', use_telegram = 0, admin_chat_ids = '[111]', event_toggles = '{}'`)

	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	NewReporter(repo, sender).NodeUsageReset(ctx, NodeReport{Name: "node-1", Actor: "pouria"})
	if calls != 0 {
		t.Fatalf("expected disabled Telegram to skip, got %d calls", calls)
	}
}

func assertNullOrEmpty(t *testing.T, db *sql.DB, column string) {
	t.Helper()
	var value sql.NullString
	if err := db.QueryRow(`SELECT ` + column + ` FROM telegram_settings WHERE id = 1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value.Valid && strings.TrimSpace(value.String) != "" {
		t.Fatalf("expected %s to be empty, got %q", column, value.String)
	}
}
