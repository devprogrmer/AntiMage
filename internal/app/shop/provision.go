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

// Account is the small set of fields needed to prove a retried provision
// belongs to the same paid order. The marker is stored in the user's note.
type Account struct {
	Username        string
	TelegramID      int64
	ServiceID       int64
	DataLimitBytes  int64
	HoldDurationSec int64
	OrderMarker     string
}

type AccountProvisioner interface {
	Lookup(ctx context.Context, username string) (Account, bool, error)
	Create(ctx context.Context, account Account) error
}

// Provision creates the user after payment. The username and order marker are
// deterministic; a crash after account creation can be reconciled by lookup.
// The order is never marked complete until the existing account is verified.
func (s Orders) Provision(ctx context.Context, orderID string, provisioner AccountProvisioner) (string, error) {
	if s.DB == nil || provisioner == nil || orderID == "" {
		return "", fmt.Errorf("invalid provisioning request")
	}
	var telegramID, serviceID, dataLimit, days int64
	var status string
	var savedUsername sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT telegram_id, service_id, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE id = ?`, orderID).
		Scan(&telegramID, &serviceID, &dataLimit, &days, &status, &savedUsername)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrOrderUnavailable
	}
	if err != nil {
		return "", err
	}
	if status != "paid" && status != "provisioning" && status != "completed" {
		return "", ErrOrderUnavailable
	}
	if days <= 0 || days > math.MaxInt64/86400 || serviceID <= 0 || dataLimit <= 0 || telegramID <= 0 {
		return "", fmt.Errorf("invalid paid order snapshot")
	}
	compact := strings.ReplaceAll(orderID, "-", "")
	if len(compact) != 32 {
		return "", fmt.Errorf("invalid order ID")
	}
	username := "tg" + compact[:30]
	if savedUsername.Valid && savedUsername.String != username {
		return "", ErrReferenceConflict
	}
	if status == "completed" {
		return username, nil
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE shop_orders SET status = 'provisioning', username = ?, updated_at = ? WHERE id = ? AND status = 'paid'`, username, time.Now().UTC().Format("2006-01-02 15:04:05"), orderID)
	if err != nil {
		return "", err
	}
	if _, err := result.RowsAffected(); err != nil {
		return "", err
	}
	want := Account{
		Username: username, TelegramID: telegramID, ServiceID: serviceID,
		DataLimitBytes: dataLimit, HoldDurationSec: days * 86400,
		OrderMarker: "shop-order:" + orderID,
	}
	account, found, err := provisioner.Lookup(ctx, username)
	if err != nil {
		return "", err
	}
	if !found {
		if createErr := provisioner.Create(ctx, want); createErr != nil {
			// A concurrent attempt may have created the user just before this call.
			account, found, err = provisioner.Lookup(ctx, username)
			if err != nil {
				return "", err
			}
			if !found {
				return "", createErr
			}
		} else {
			account, found, err = provisioner.Lookup(ctx, username)
			if err != nil {
				return "", err
			}
			if !found {
				return "", fmt.Errorf("account creation was not visible")
			}
		}
	}
	// The first connection consumes on_hold_expire_duration and activates
	// the account. Its immutable ownership and order marker must still match.
	if account.Username != want.Username || account.TelegramID != want.TelegramID ||
		account.ServiceID != want.ServiceID || account.DataLimitBytes != want.DataLimitBytes ||
		account.OrderMarker != want.OrderMarker ||
		(account.HoldDurationSec != want.HoldDurationSec && account.HoldDurationSec != 0) {
		return "", ErrReferenceConflict
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE shop_orders SET status = 'completed', updated_at = ? WHERE id = ? AND status = 'provisioning' AND username = ?`, time.Now().UTC().Format("2006-01-02 15:04:05"), orderID, username)
	if err != nil {
		return "", err
	}
	return username, nil
}
