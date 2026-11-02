package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrProductUnavailable = errors.New("shop product unavailable")

type Product struct {
	ID             int64
	ServiceID      int64
	Title          string
	PriceToman     int64
	DataLimitBytes int64
	DurationDays   int64
	Enabled        bool
}

type Order struct {
	ID             string
	RequestKey     string
	TelegramID     int64
	ProductID      int64
	ServiceID      int64
	PriceToman     int64
	DataLimitBytes int64
	DurationDays   int64
	Status         string
}

type Orders struct{ DB *sql.DB }

func (s Orders) ListProducts(ctx context.Context) ([]Product, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, service_id, title, price_toman, data_limit_bytes, duration_days, enabled FROM shop_products WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.ServiceID, &p.Title, &p.PriceToman, &p.DataLimitBytes, &p.DurationDays, &p.Enabled); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

// Place snapshots the price and service parameters once. Repeated Telegram
// callbacks with the same request key return the original order.
func (s Orders) Place(ctx context.Context, telegramID, productID int64, requestKey string) (Order, error) {
	if s.DB == nil || telegramID <= 0 || productID <= 0 || requestKey == "" || len(requestKey) > 128 || strings.TrimSpace(requestKey) != requestKey {
		return Order{}, fmt.Errorf("invalid order request")
	}
	if existing, found, err := s.byKey(ctx, requestKey); err != nil {
		return Order{}, err
	} else if found {
		if existing.TelegramID != telegramID || existing.ProductID != productID {
			return Order{}, ErrReferenceConflict
		}
		return existing, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	id := uuid.NewString()
	result, err := tx.ExecContext(ctx, `INSERT INTO shop_orders (id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, created_at, updated_at)
		SELECT ?, ?, ?, id, service_id, price_toman, data_limit_bytes, duration_days, 'pending', ?, ? FROM shop_products WHERE id = ? AND enabled = 1`, id, requestKey, telegramID, now, now, productID)
	if err != nil {
		_ = tx.Rollback()
		if existing, found, lookupErr := s.byKey(ctx, requestKey); lookupErr == nil && found {
			if existing.TelegramID != telegramID || existing.ProductID != productID {
				return Order{}, ErrReferenceConflict
			}
			return existing, nil
		}
		return Order{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Order{}, err
	}
	if affected != 1 {
		return Order{}, ErrProductUnavailable
	}
	order, err := scanOrder(tx.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status FROM shop_orders WHERE id = ?`, id))
	if err != nil {
		return Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s Orders) byKey(ctx context.Context, key string) (Order, bool, error) {
	order, err := scanOrder(s.DB.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status FROM shop_orders WHERE request_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	return order, err == nil, err
}

func scanOrder(row *sql.Row) (Order, error) {
	var order Order
	err := row.Scan(&order.ID, &order.RequestKey, &order.TelegramID, &order.ProductID, &order.ServiceID, &order.PriceToman, &order.DataLimitBytes, &order.DurationDays, &order.Status)
	return order, err
}
