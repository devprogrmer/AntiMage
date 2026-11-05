package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrTopupUnavailable = errors.New("topup request unavailable")

type Topup struct {
	ID            string
	TelegramID    int64
	AmountToman   int64
	Status        string
	ReceiptFileID *string
	ReviewedBy    *int64
}

type Topups struct {
	DB      *sql.DB
	Dialect string
}

func (s Topups) List(ctx context.Context, telegramID int64, adminPending bool) ([]Topup, error) {
	if s.DB == nil || (!adminPending && telegramID <= 0) {
		return nil, fmt.Errorf("invalid topup list")
	}
	query := `SELECT id, telegram_id, amount_toman, status, receipt_file_id, reviewed_by FROM shop_topup_requests WHERE telegram_id = ? ORDER BY created_at DESC LIMIT 20`
	args := []any{telegramID}
	if adminPending {
		query = `SELECT id, telegram_id, amount_toman, status, receipt_file_id, reviewed_by FROM shop_topup_requests WHERE status IN ('submitted', 'approving') ORDER BY created_at DESC LIMIT 20`
		args = nil
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Topup{}
	for rows.Next() {
		var item Topup
		if err := rows.Scan(&item.ID, &item.TelegramID, &item.AmountToman, &item.Status, &item.ReceiptFileID, &item.ReviewedBy); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Topups) Create(ctx context.Context, telegramID, amount int64, requestKey string) (Topup, error) {
	if s.DB == nil || telegramID <= 0 || amount <= 0 || !validTopupReference(requestKey) {
		return Topup{}, fmt.Errorf("invalid topup request")
	}
	if existing, ok, err := s.byReference(ctx, requestKey); err != nil {
		return Topup{}, err
	} else if ok {
		if existing.TelegramID != telegramID || existing.AmountToman != amount {
			return Topup{}, ErrReferenceConflict
		}
		return existing, nil
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	id := uuid.NewString()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO shop_topup_requests (id, telegram_id, amount_toman, status, request_key, created_at, updated_at) VALUES (?, ?, ?, 'pending', ?, ?, ?)`, id, telegramID, amount, requestKey, now, now)
	if err != nil {
		if existing, ok, lookupErr := s.byReference(ctx, requestKey); lookupErr == nil && ok {
			if existing.TelegramID != telegramID || existing.AmountToman != amount {
				return Topup{}, ErrReferenceConflict
			}
			return existing, nil
		}
		return Topup{}, err
	}
	return Topup{ID: id, TelegramID: telegramID, AmountToman: amount, Status: "pending"}, nil
}

func (s Topups) SubmitReceipt(ctx context.Context, telegramID int64, topupID, fileID string) (Topup, error) {
	if telegramID <= 0 || len(topupID) != 36 || fileID == "" || len(fileID) > 512 {
		return Topup{}, ErrTopupUnavailable
	}
	topup, err := s.Get(ctx, topupID)
	if err != nil || topup.TelegramID != telegramID {
		return Topup{}, ErrTopupUnavailable
	}
	if topup.Status == "submitted" && topup.ReceiptFileID != nil && *topup.ReceiptFileID == fileID {
		return topup, nil
	}
	if topup.Status != "pending" {
		return Topup{}, ErrTopupUnavailable
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE shop_topup_requests SET status = 'submitted', receipt_file_id = ?, updated_at = ? WHERE id = ? AND telegram_id = ? AND status = 'pending'`, fileID, time.Now().UTC().Format("2006-01-02 15:04:05"), topupID, telegramID)
	if err != nil {
		return Topup{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Topup{}, err
	}
	if affected != 1 {
		return Topup{}, ErrTopupUnavailable
	}
	return s.Get(ctx, topupID)
}

// Approve claims the request before crediting. Rejection can no longer race
// with an in-progress credit. A crash between credit and final status is safe
// because the wallet reference is unique and retries are idempotent.
func (s Topups) Approve(ctx context.Context, topupID string, adminID int64, wallet Wallet) (Topup, error) {
	if s.DB == nil || wallet.DB != s.DB || len(topupID) != 36 || adminID <= 0 {
		return Topup{}, ErrTopupUnavailable
	}
	topup, err := s.Get(ctx, topupID)
	if err != nil {
		return Topup{}, err
	}
	if topup.Status == "approved" {
		return topup, nil
	}
	if topup.Status == "submitted" {
		result, err := s.DB.ExecContext(ctx, `UPDATE shop_topup_requests SET status = 'approving', reviewed_by = ?, updated_at = ? WHERE id = ? AND status = 'submitted' AND receipt_file_id IS NOT NULL`, adminID, time.Now().UTC().Format("2006-01-02 15:04:05"), topupID)
		if err != nil {
			return Topup{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Topup{}, err
		}
		if affected != 1 {
			topup, err = s.Get(ctx, topupID)
			if err != nil {
				return Topup{}, err
			}
		} else {
			topup.Status = "approving"
		}
	}
	if topup.Status == "approved" {
		return topup, nil
	}
	if topup.Status != "submitted" && topup.Status != "approving" {
		return Topup{}, ErrTopupUnavailable
	}
	if _, err := wallet.Credit(ctx, topup.TelegramID, topup.AmountToman, "topup:"+topupID); err != nil {
		return Topup{}, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE shop_topup_requests SET status = 'approved', updated_at = ? WHERE id = ? AND status = 'approving'`, time.Now().UTC().Format("2006-01-02 15:04:05"), topupID); err != nil {
		return Topup{}, err
	}
	return s.Get(ctx, topupID)
}

func (s Topups) Reject(ctx context.Context, topupID string, adminID int64) (Topup, error) {
	if s.DB == nil || len(topupID) != 36 || adminID <= 0 {
		return Topup{}, ErrTopupUnavailable
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE shop_topup_requests SET status = 'rejected', reviewed_by = ?, updated_at = ? WHERE id = ? AND status = 'submitted'`, adminID, time.Now().UTC().Format("2006-01-02 15:04:05"), topupID)
	if err != nil {
		return Topup{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Topup{}, err
	}
	if affected != 1 {
		return Topup{}, ErrTopupUnavailable
	}
	return s.Get(ctx, topupID)
}

func (s Topups) Get(ctx context.Context, id string) (Topup, error) {
	if s.DB == nil || len(id) != 36 {
		return Topup{}, ErrTopupUnavailable
	}
	return scanTopup(s.DB.QueryRowContext(ctx, `SELECT id, telegram_id, amount_toman, status, receipt_file_id, reviewed_by FROM shop_topup_requests WHERE id = ?`, id))
}

func (s Topups) byReference(ctx context.Context, reference string) (Topup, bool, error) {
	topup, err := scanTopup(s.DB.QueryRowContext(ctx, `SELECT id, telegram_id, amount_toman, status, receipt_file_id, reviewed_by FROM shop_topup_requests WHERE request_key = ?`, reference))
	if errors.Is(err, ErrTopupUnavailable) {
		return Topup{}, false, nil
	}
	return topup, err == nil, err
}

func scanTopup(row *sql.Row) (Topup, error) {
	var topup Topup
	err := row.Scan(&topup.ID, &topup.TelegramID, &topup.AmountToman, &topup.Status, &topup.ReceiptFileID, &topup.ReviewedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Topup{}, ErrTopupUnavailable
	}
	return topup, err
}

func (s Topups) Instructions(ctx context.Context) (string, error) {
	if s.DB == nil {
		return "", fmt.Errorf("topup store unavailable")
	}
	var instructions string
	err := s.DB.QueryRowContext(ctx, `SELECT instructions FROM shop_payment_settings WHERE id = 1`).Scan(&instructions)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return instructions, err
}

func (s Topups) SetInstructions(ctx context.Context, instructions string) error {
	if s.DB == nil || utf8.RuneCountInString(instructions) > 1000 {
		return fmt.Errorf("invalid payment instructions")
	}
	instructions = strings.TrimSpace(instructions)
	query := `INSERT INTO shop_payment_settings (id, instructions) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET instructions = excluded.instructions`
	if strings.EqualFold(s.Dialect, "mysql") {
		query = `INSERT INTO shop_payment_settings (id, instructions) VALUES (1, ?) ON DUPLICATE KEY UPDATE instructions = VALUES(instructions)`
	}
	_, err := s.DB.ExecContext(ctx, query, instructions)
	return err
}

func validTopupReference(reference string) bool {
	return reference != "" && len(reference) <= 128 && strings.TrimSpace(reference) == reference
}
