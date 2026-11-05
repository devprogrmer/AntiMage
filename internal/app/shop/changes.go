package shop

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type OrderChange struct {
	Kind     string
	Username string
}

func (s Orders) PlaceChange(ctx context.Context, telegramID, productID int64, username, kind, requestKey string) (Order, error) {
	if (kind != "renew" && kind != "volume") || username == "" || len(username) > 34 || strings.TrimSpace(username) != username {
		return Order{}, ErrOrderUnavailable
	}
	return s.place(ctx, telegramID, productID, requestKey, &OrderChange{Kind: kind, Username: username})
}

func (s Orders) Change(ctx context.Context, orderID string) (*OrderChange, error) {
	var change OrderChange
	err := s.DB.QueryRowContext(ctx, `SELECT kind, username FROM shop_order_changes WHERE order_id=?`, orderID).Scan(&change.Kind, &change.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &change, err
}

func (s Orders) checkChangeReplay(ctx context.Context, orderID string, want *OrderChange) error {
	if want == nil {
		return nil
	}
	got, err := s.Change(ctx, orderID)
	if err != nil {
		return err
	}
	if got == nil || *got != *want {
		return ErrReferenceConflict
	}
	return nil
}

// FulfillChange commits the account mutation, its node outbox, and order
// completion in one transaction. The callback must use tx for every write.
func (s Orders) FulfillChange(ctx context.Context, orderID string, apply func(context.Context, *sql.Tx, Order, OrderChange) error) (string, error) {
	if s.DB == nil || apply == nil {
		return "", ErrOrderUnavailable
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE shop_orders SET status=status WHERE id=?`, orderID); err != nil {
		return "", err
	}
	order, err := scanOrder(tx.QueryRowContext(ctx, `SELECT id,request_key,telegram_id,product_id,service_id,price_toman,data_limit_bytes,duration_days,status,username FROM shop_orders WHERE id=?`, orderID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrOrderUnavailable
	}
	if err != nil {
		return "", err
	}
	var change OrderChange
	if err := tx.QueryRowContext(ctx, `SELECT kind,username FROM shop_order_changes WHERE order_id=?`, orderID).Scan(&change.Kind, &change.Username); err != nil {
		return "", err
	}
	if order.Username == nil || *order.Username != change.Username || (change.Kind != "renew" && change.Kind != "volume") {
		return "", ErrReferenceConflict
	}
	if order.Status == "completed" {
		return change.Username, nil
	}
	if order.Status != "paid" && order.Status != "provisioning" {
		return "", ErrOrderUnavailable
	}
	if err := apply(ctx, tx, order, change); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shop_orders SET status='completed',updated_at=? WHERE id=?`, time.Now().UTC().Format("2006-01-02 15:04:05"), orderID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return change.Username, nil
}
