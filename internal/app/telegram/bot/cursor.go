package bot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// The token itself never enters the database. Keeping a cursor per token also
// prevents a replacement bot from inheriting another bot's update offset.
func tokenCursorKey(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

func (s stateStore) updateOffset(ctx context.Context, token string) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	var offset int64
	err := s.db.QueryRowContext(ctx, `SELECT next_update_id FROM telegram_bot_cursor WHERE token_hash = ?`, tokenCursorKey(token)).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return offset, err
}

func (s stateStore) saveUpdateOffset(ctx context.Context, token string, offset int64) error {
	if s.db == nil {
		return nil
	}
	if offset < 0 {
		return fmt.Errorf("invalid Telegram update offset")
	}
	key := tokenCursorKey(token)
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_bot_cursor SET next_update_id = CASE WHEN next_update_id < ? THEN ? ELSE next_update_id END WHERE token_hash = ?`, offset, offset, key)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected > 0 {
		return nil
	}
	// Some drivers report zero affected rows for an unchanged value.
	var existing int64
	err = s.db.QueryRowContext(ctx, `SELECT next_update_id FROM telegram_bot_cursor WHERE token_hash = ?`, key).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO telegram_bot_cursor (token_hash, next_update_id) VALUES (?, ?)`, key, offset); err != nil {
		// A concurrent writer may have inserted the key. Preserve the greater
		// offset and surface any other failure.
		if _, retryErr := s.db.ExecContext(ctx, `UPDATE telegram_bot_cursor SET next_update_id = CASE WHEN next_update_id < ? THEN ? ELSE next_update_id END WHERE token_hash = ?`, offset, offset, key); retryErr != nil {
			return retryErr
		}
		if queryErr := s.db.QueryRowContext(ctx, `SELECT next_update_id FROM telegram_bot_cursor WHERE token_hash = ?`, key).Scan(&existing); queryErr != nil {
			return err
		}
	}
	return nil
}
