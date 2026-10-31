package user

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
)

type SubscriptionHWID struct {
	Hash        string `json:"hwid_hash"`
	FirstUsedAt string `json:"first_used_at"`
	LastUsedAt  string `json:"last_used_at"`
}

func (s Service) ListSubscriptionHWIDs(ctx context.Context, userID int64) ([]SubscriptionHWID, error) {
	rows, err := s.repo.db.QueryContext(ctx, `SELECT hwid_hash, first_used_at, last_used_at FROM user_subscription_hwids WHERE user_id = ? ORDER BY last_used_at DESC, hwid_hash`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SubscriptionHWID, 0)
	for rows.Next() {
		var item SubscriptionHWID
		if err := rows.Scan(&item.Hash, &item.FirstUsedAt, &item.LastUsedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) ResetSubscriptionHWIDs(ctx context.Context, userID int64) error {
	_, err := s.repo.db.ExecContext(ctx, `DELETE FROM user_subscription_hwids WHERE user_id = ?`, userID)
	return err
}

// registerSubscriptionHWID applies device_limit to Xray-style subscriptions.
// Only a one-way digest is persisted; clients must supply a stable X-HWID.
func (r Repository) registerSubscriptionHWID(ctx context.Context, userID int64, rawHWID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT COALESCE(device_limit, 0) FROM users WHERE id = ?`
	if !strings.EqualFold(strings.TrimSpace(r.dialect), "sqlite") {
		query += ` FOR UPDATE`
	}
	var limit int64
	if err := tx.QueryRowContext(ctx, query, userID).Scan(&limit); err != nil {
		if err == sql.ErrNoRows {
			return clientError(404, "Not Found")
		}
		return err
	}
	if limit <= 0 {
		return tx.Commit()
	}
	rawHWID = strings.TrimSpace(rawHWID)
	if len(rawHWID) == 0 || len(rawHWID) > 255 {
		return clientError(403, "HWID header required")
	}
	for _, ch := range rawHWID {
		if ch < 0x21 || ch > 0x7e {
			return clientError(403, "Invalid HWID header")
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(rawHWID)))
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM user_subscription_hwids WHERE user_id = ? AND hwid_hash = ?`, userID, digest).Scan(&exists)
	if err == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE user_subscription_hwids SET last_used_at = CURRENT_TIMESTAMP WHERE user_id = ? AND hwid_hash = ?`, userID, digest); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_subscription_hwids WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return clientError(403, "Device limit reached")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_subscription_hwids (user_id, hwid_hash, first_used_at, last_used_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, userID, digest); err != nil {
		return err
	}
	return tx.Commit()
}
