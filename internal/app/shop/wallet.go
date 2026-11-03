package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrInsufficientFunds = errors.New("insufficient wallet funds")
	ErrReferenceConflict = errors.New("wallet reference already used for a different transaction")
	ErrBalanceOverflow   = errors.New("wallet balance overflow")
)

// Wallet stores the balance and its immutable journal in the same transaction.
// A reference is globally unique so a retried payment cannot credit twice.
type Wallet struct {
	DB      *sql.DB
	Dialect string
}

type WalletEntry struct {
	Reference string
	Delta     int64
	Reason    string
	CreatedAt string
}

func (w Wallet) History(ctx context.Context, telegramID int64, limit int) ([]WalletEntry, error) {
	if w.DB == nil || telegramID <= 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid wallet history request")
	}
	rows, err := w.DB.QueryContext(ctx, `SELECT reference, delta, reason, created_at FROM shop_wallet_entries
		WHERE telegram_id = ? ORDER BY created_at DESC, reference DESC LIMIT ?`, telegramID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []WalletEntry{}
	for rows.Next() {
		var entry WalletEntry
		if err := rows.Scan(&entry.Reference, &entry.Delta, &entry.Reason, &entry.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (w Wallet) Balance(ctx context.Context, telegramID int64) (int64, error) {
	if telegramID <= 0 {
		return 0, fmt.Errorf("invalid telegram ID")
	}
	var balance int64
	err := w.DB.QueryRowContext(ctx, `SELECT balance FROM shop_wallet_accounts WHERE telegram_id = ?`, telegramID).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return balance, err
}

func (w Wallet) Credit(ctx context.Context, telegramID, amount int64, reference string) (int64, error) {
	return w.mutate(ctx, telegramID, amount, reference, "topup")
}

func (w Wallet) Debit(ctx context.Context, telegramID, amount int64, reference string) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	return w.mutate(ctx, telegramID, -amount, reference, "purchase")
}

func (w Wallet) mutate(ctx context.Context, telegramID, delta int64, reference, reason string) (int64, error) {
	if w.DB == nil || telegramID <= 0 || delta == 0 || len(reference) > 128 || strings.TrimSpace(reference) != reference || reference == "" {
		return 0, fmt.Errorf("invalid wallet mutation")
	}
	if balance, found, err := w.existing(ctx, telegramID, delta, reference, reason); err != nil || found {
		return balance, err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ensure := `INSERT INTO shop_wallet_accounts (telegram_id, balance) VALUES (?, 0) ON CONFLICT(telegram_id) DO NOTHING`
	if strings.EqualFold(w.Dialect, "mysql") {
		ensure = `INSERT INTO shop_wallet_accounts (telegram_id, balance) VALUES (?, 0) ON DUPLICATE KEY UPDATE telegram_id = telegram_id`
	}
	if _, err := tx.ExecContext(ctx, ensure, telegramID); err != nil {
		return 0, err
	}
	var result sql.Result
	if delta > 0 {
		result, err = tx.ExecContext(ctx, `UPDATE shop_wallet_accounts SET balance = balance + ? WHERE telegram_id = ? AND balance <= ?`, delta, telegramID, math.MaxInt64-delta)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE shop_wallet_accounts SET balance = balance + ? WHERE telegram_id = ? AND balance >= ?`, delta, telegramID, -delta)
	}
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		_ = tx.Rollback()
		if balance, found, existingErr := w.existing(ctx, telegramID, delta, reference, reason); existingErr != nil || found {
			return balance, existingErr
		}
		if delta < 0 {
			return 0, ErrInsufficientFunds
		}
		return 0, ErrBalanceOverflow
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO shop_wallet_entries (reference, telegram_id, delta, reason, created_at) VALUES (?, ?, ?, ?, ?)`, reference, telegramID, delta, reason, time.Now().UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		_ = tx.Rollback()
		if balance, found, existingErr := w.existing(ctx, telegramID, delta, reference, reason); existingErr != nil || found {
			return balance, existingErr
		}
		return 0, err
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, `SELECT balance FROM shop_wallet_accounts WHERE telegram_id = ?`, telegramID).Scan(&balance); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return balance, nil
}

func (w Wallet) existing(ctx context.Context, telegramID, delta int64, reference, reason string) (int64, bool, error) {
	var owner, savedDelta int64
	var savedReason string
	err := w.DB.QueryRowContext(ctx, `SELECT telegram_id, delta, reason FROM shop_wallet_entries WHERE reference = ?`, reference).Scan(&owner, &savedDelta, &savedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if owner != telegramID || savedDelta != delta || savedReason != reason {
		return 0, true, ErrReferenceConflict
	}
	balance, err := w.Balance(ctx, telegramID)
	return balance, true, err
}
