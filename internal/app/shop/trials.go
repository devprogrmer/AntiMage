package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrTrialUnavailable = errors.New("trial unavailable")

type TrialConfig struct {
	ServiceID      int64
	DataLimitBytes int64
	DurationDays   int64
}

type TrialClaim struct {
	TelegramID     int64
	ClaimID        string
	ServiceID      int64
	DataLimitBytes int64
	DurationDays   int64
	Status         string
	Username       sql.NullString
}

type Trials struct {
	DB      *sql.DB
	Dialect string
}

func (s Trials) Configure(ctx context.Context, config TrialConfig) error {
	if s.DB == nil || config.ServiceID <= 0 || config.DataLimitBytes <= 0 || config.DurationDays <= 0 || config.DurationDays > math.MaxInt64/86400 {
		return fmt.Errorf("invalid trial configuration")
	}
	query := `INSERT INTO shop_trial_config (id, service_id, data_limit_bytes, duration_days, enabled) VALUES (1, ?, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET service_id = excluded.service_id, data_limit_bytes = excluded.data_limit_bytes, duration_days = excluded.duration_days, enabled = 1`
	if strings.EqualFold(s.Dialect, "mysql") {
		query = `INSERT INTO shop_trial_config (id, service_id, data_limit_bytes, duration_days, enabled) VALUES (1, ?, ?, ?, 1)
			ON DUPLICATE KEY UPDATE service_id = VALUES(service_id), data_limit_bytes = VALUES(data_limit_bytes), duration_days = VALUES(duration_days), enabled = 1`
	}
	_, err := s.DB.ExecContext(ctx, query, config.ServiceID, config.DataLimitBytes, config.DurationDays)
	return err
}

func (s Trials) Disable(ctx context.Context) error {
	if s.DB == nil {
		return fmt.Errorf("trial store is unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE shop_trial_config SET enabled = 0 WHERE id = 1`)
	return err
}

func (s Trials) Claim(ctx context.Context, telegramID int64) (TrialClaim, error) {
	if s.DB == nil || telegramID <= 0 {
		return TrialClaim{}, fmt.Errorf("invalid trial claimant")
	}
	if existing, err := s.Get(ctx, telegramID); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TrialClaim{}, err
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	result, err := s.DB.ExecContext(ctx, `INSERT INTO shop_trial_claims (telegram_id, claim_id, service_id, data_limit_bytes, duration_days, status, created_at, updated_at)
		SELECT ?, ?, service_id, data_limit_bytes, duration_days, 'pending', ?, ? FROM shop_trial_config WHERE id = 1 AND enabled = 1`, telegramID, id, now, now)
	if err != nil {
		// The other request may have won the one-trial-per-Telegram-ID race.
		if existing, lookupErr := s.Get(ctx, telegramID); lookupErr == nil {
			return existing, nil
		}
		return TrialClaim{}, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return TrialClaim{}, err
	} else if count != 1 {
		return TrialClaim{}, ErrTrialUnavailable
	}
	return s.Get(ctx, telegramID)
}

func (s Trials) Get(ctx context.Context, telegramID int64) (TrialClaim, error) {
	if s.DB == nil || telegramID <= 0 {
		return TrialClaim{}, fmt.Errorf("invalid trial claimant")
	}
	var claim TrialClaim
	err := s.DB.QueryRowContext(ctx, `SELECT telegram_id, claim_id, service_id, data_limit_bytes, duration_days, status, username FROM shop_trial_claims WHERE telegram_id = ?`, telegramID).
		Scan(&claim.TelegramID, &claim.ClaimID, &claim.ServiceID, &claim.DataLimitBytes, &claim.DurationDays, &claim.Status, &claim.Username)
	return claim, err
}

func (s Trials) Unfulfilled(ctx context.Context, limit int) ([]int64, error) {
	if s.DB == nil || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("invalid trial recovery limit")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT telegram_id FROM shop_trial_claims WHERE status IN ('pending', 'provisioning') ORDER BY updated_at, telegram_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Provision is retryable after a crash because username and marker are fixed
// by the claim. A second attempt must verify ownership before completing it.
func (s Trials) Provision(ctx context.Context, telegramID int64, provisioner AccountProvisioner) (string, error) {
	if provisioner == nil {
		return "", fmt.Errorf("trial account provisioner is unavailable")
	}
	claim, err := s.Get(ctx, telegramID)
	if err != nil {
		return "", err
	}
	if claim.Status != "pending" && claim.Status != "provisioning" && claim.Status != "completed" {
		return "", ErrTrialUnavailable
	}
	if claim.ServiceID <= 0 || claim.DataLimitBytes <= 0 || claim.DurationDays <= 0 || claim.DurationDays > math.MaxInt64/86400 {
		return "", fmt.Errorf("invalid trial snapshot")
	}
	parsed, err := uuid.Parse(claim.ClaimID)
	if err != nil {
		return "", err
	}
	username := "tr" + strings.ReplaceAll(parsed.String(), "-", "")
	if claim.Username.Valid && claim.Username.String != username {
		return "", ErrReferenceConflict
	}
	if claim.Status == "completed" {
		return username, nil
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE shop_trial_claims SET status = 'provisioning', username = ?, updated_at = ? WHERE telegram_id = ? AND status = 'pending'`, username, time.Now().UTC().Format("2006-01-02 15:04:05"), telegramID)
	if err != nil {
		return "", err
	}
	want := Account{Username: username, TelegramID: telegramID, ServiceID: claim.ServiceID, DataLimitBytes: claim.DataLimitBytes, HoldDurationSec: claim.DurationDays * 86400, OrderMarker: "shop-trial:" + claim.ClaimID}
	account, found, err := provisioner.Lookup(ctx, username)
	if err != nil {
		return "", err
	}
	if !found {
		createErr := provisioner.Create(ctx, want)
		account, found, err = provisioner.Lookup(ctx, username)
		if err != nil {
			return "", err
		}
		if !found {
			if createErr != nil {
				return "", createErr
			}
			return "", fmt.Errorf("trial account creation was not visible")
		}
	}
	if account.Username != want.Username || account.TelegramID != want.TelegramID || account.ServiceID != want.ServiceID || account.DataLimitBytes != want.DataLimitBytes || account.OrderMarker != want.OrderMarker ||
		(account.HoldDurationSec != want.HoldDurationSec && account.HoldDurationSec != 0) {
		return "", ErrReferenceConflict
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE shop_trial_claims SET status = 'completed', updated_at = ? WHERE telegram_id = ? AND status = 'provisioning' AND username = ?`, time.Now().UTC().Format("2006-01-02 15:04:05"), telegramID, username)
	return username, err
}
