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

func TestPruneOutboxRemovesOnlyBoundedOldTerminalRows(t *testing.T) {
	ctx := context.Background()
	db, repo := testTelegramRepo(t)
	for _, event := range []string{"sent-old", "failed-old", "pending-old", "sent-new"} {
		if err := repo.EnqueueReport(ctx, event, "message"); err != nil {
			t.Fatal(err)
		}
	}
	old := dbTime(time.Now().Add(-60 * 24 * time.Hour))
	newer := dbTime(time.Now().Add(-time.Hour))
	if _, err := db.Exec(`UPDATE telegram_message_outbox SET status='sent',updated_at=? WHERE event='sent-old'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE telegram_message_outbox SET status='failed',updated_at=? WHERE event='failed-old'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE telegram_message_outbox SET status='pending',updated_at=? WHERE event='pending-old'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE telegram_message_outbox SET status='sent',updated_at=? WHERE event='sent-new'`, newer); err != nil {
		t.Fatal(err)
	}

	deleted, err := repo.PruneOutbox(ctx, time.Now().Add(-30*24*time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want bounded single row", deleted)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telegram_message_outbox`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 3 {
		t.Fatalf("remaining rows=%d, want 3", remaining)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telegram_message_outbox WHERE event='pending-old' AND status='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatal("pruning removed a pending delivery")
	}
}
