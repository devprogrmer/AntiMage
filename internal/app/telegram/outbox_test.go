package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOutboxRetriesAndRecoversExpiredLease(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")
	if err := repo.EnqueueReport(ctx, "user.created", "<b>created</b>"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE telegram_message_outbox SET status = 'processing', claimed_at = ?`, dbTime(time.Now().Add(-2*defaultOutboxLease))); err != nil {
		t.Fatal(err)
	}

	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()
	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	if err := NewOutboxDispatcher(repo, sender).Dispatch(ctx, 10); err != nil {
		t.Fatalf("dispatch expired lease: %v", err)
	}
	if calls != 1 {
		t.Fatalf("send calls = %d, want 1", calls)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM telegram_message_outbox`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("status = %q, want sent", status)
	}
}

func TestOutboxFailureIsPersistedAndRescheduled(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	seedTelegramSettings(t, db, "")
	if err := repo.EnqueueReport(ctx, "login", "login failed"); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"description":"temporarily unavailable"}`))
	}))
	defer api.Close()
	sender := NewSender(repo, api.URL)
	sender.retryDelays = nil
	if err := NewOutboxDispatcher(repo, sender).Dispatch(ctx, 10); err == nil {
		t.Fatal("expected send failure")
	}
	var status string
	var attempts int
	var lastError string
	if err := db.QueryRow(`SELECT status, attempts, last_error FROM telegram_message_outbox`).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || lastError == "" {
		t.Fatalf("failed delivery state status=%q attempts=%d error=%q", status, attempts, lastError)
	}
}
