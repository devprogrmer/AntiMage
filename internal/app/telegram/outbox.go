package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultOutboxBatchSize = 32
	defaultOutboxMaxTries  = 8
	defaultOutboxRetry     = 15 * time.Second
	defaultOutboxLease     = time.Minute
)

type queuedMessage struct {
	id       int64
	event    string
	message  string
	attempts int
}

// EnqueueReport persists a rendered, non-secret notification. Credentials and
// destinations are deliberately resolved by Sender at delivery time.
func (r Repository) EnqueueReport(ctx context.Context, event, message string) error {
	if r.db == nil {
		return nil
	}
	event = strings.TrimSpace(event)
	message = strings.TrimSpace(message)
	if event == "" || message == "" {
		return fmt.Errorf("telegram outbox event and message are required")
	}
	now := dbTime(time.Now().UTC())
	_, err := r.db.ExecContext(ctx, `INSERT INTO telegram_message_outbox
(event, message, status, attempts, send_at, created_at, updated_at)
VALUES (?, ?, 'pending', 0, ?, ?, ?)`, event, message, now, now, now)
	return err
}

// claimDue atomically reserves due messages. Expired processing leases are
// returned to pending so a process crash cannot strand the outbox.
func (r Repository) claimDue(ctx context.Context, limit int, now time.Time) ([]queuedMessage, error) {
	if r.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultOutboxBatchSize
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowText := dbTime(now.UTC())
	leaseText := dbTime(now.UTC().Add(-defaultOutboxLease))
	if _, err := tx.ExecContext(ctx, `UPDATE telegram_message_outbox
SET status = 'pending', claimed_at = NULL, updated_at = ?
WHERE status = 'processing' AND claimed_at < ?`, nowText, leaseText); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, event, message, attempts
FROM telegram_message_outbox
WHERE status = 'pending' AND send_at <= ?
ORDER BY id LIMIT ?`, nowText, limit)
	if err != nil {
		return nil, err
	}
	items := make([]queuedMessage, 0, limit)
	for rows.Next() {
		var item queuedMessage
		if err := rows.Scan(&item.id, &item.event, &item.message, &item.attempts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claimed := items[:0]
	for _, item := range items {
		result, err := tx.ExecContext(ctx, `UPDATE telegram_message_outbox
SET status = 'processing', claimed_at = ?, updated_at = ?
WHERE id = ? AND status = 'pending' AND send_at <= ?`, nowText, nowText, item.id, nowText)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			claimed = append(claimed, item)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (r Repository) markOutboxSent(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE telegram_message_outbox
SET status = 'sent', claimed_at = NULL, last_error = NULL, updated_at = ?
WHERE id = ? AND status = 'processing'`, dbTime(time.Now().UTC()), id)
	return err
}

func (r Repository) refreshOutboxLease(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE telegram_message_outbox
SET claimed_at = ?, updated_at = ?
WHERE id = ? AND status = 'processing'`, dbTime(time.Now().UTC()), dbTime(time.Now().UTC()), id)
	return err
}

// PruneOutbox removes a bounded number of old terminal messages while keeping
// pending and processing delivery records intact.
func (r Repository) PruneOutbox(ctx context.Context, before time.Time, limit int) (int, error) {
	if r.db == nil {
		return 0, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM telegram_message_outbox
WHERE status IN ('sent', 'failed') AND updated_at < ?
ORDER BY id LIMIT ?`, dbTime(before.UTC()), limit)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM telegram_message_outbox
WHERE status IN ('sent', 'failed') AND id IN (`+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return len(ids), nil
	}
	return int(count), nil
}

func (r Repository) rescheduleOutbox(ctx context.Context, item queuedMessage, sendErr error, now time.Time) error {
	attempts := item.attempts + 1
	status := "pending"
	var sendAt any = dbTime(now.Add(defaultOutboxRetry).UTC())
	if attempts >= defaultOutboxMaxTries {
		status = "failed"
		sendAt = dbTime(now.UTC())
	}
	message := sendErr.Error()
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, err := r.db.ExecContext(ctx, `UPDATE telegram_message_outbox
SET status = ?, attempts = ?, send_at = ?, claimed_at = NULL, last_error = ?, updated_at = ?
WHERE id = ? AND status = 'processing'`, status, attempts, sendAt, message, dbTime(now.UTC()), item.id)
	return err
}

// OutboxDispatcher performs network delivery away from API request paths.
// Delivery is at-least-once: a crash after Telegram accepts a message but
// before the sent marker is stored can cause a retry after the lease expires.
type OutboxDispatcher struct {
	repo   Repository
	sender Sender
}

func NewOutboxDispatcher(repo Repository, sender Sender) OutboxDispatcher {
	return OutboxDispatcher{repo: repo, sender: sender}
}

func (d OutboxDispatcher) Dispatch(ctx context.Context, limit int) error {
	items, err := d.repo.claimDue(ctx, limit, time.Now().UTC())
	if err != nil || len(items) == 0 {
		return err
	}
	var failures []error
	for _, item := range items {
		stopLease := make(chan struct{})
		leaseDone := make(chan struct{})
		go d.keepOutboxLease(ctx, item.id, stopLease, leaseDone)
		_, sendErr := d.sender.SendMessage(ctx, MessageRequest{
			Destination:           DestinationRequest{Purpose: DestinationLogs, Category: item.event},
			Text:                  item.message,
			ParseMode:             "HTML",
			DisableWebPagePreview: true,
		})
		close(stopLease)
		<-leaseDone
		if sendErr == nil {
			if err := d.repo.markOutboxSent(ctx, item.id); err != nil {
				failures = append(failures, fmt.Errorf("mark telegram message %d sent: %w", item.id, err))
			}
			continue
		}
		if err := d.repo.rescheduleOutbox(ctx, item, sendErr, time.Now().UTC()); err != nil {
			failures = append(failures, fmt.Errorf("reschedule telegram message %d: %w", item.id, err))
		}
		failures = append(failures, sendErr)
		if errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded) {
			break
		}
	}
	return errors.Join(failures...)
}

func (d OutboxDispatcher) keepOutboxLease(ctx context.Context, id int64, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(defaultOutboxLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = d.repo.refreshOutboxLease(refreshCtx, id)
			cancel()
		}
	}
}
