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
var ErrOrderUnavailable = errors.New("shop order unavailable")

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
	Username       *string
}

type Orders struct{ DB *sql.DB }

type DebitedOrder struct {
	ID         string
	TelegramID int64
}

type SalesOverview struct {
	TotalOrders       int64
	PendingOrders     int64
	UnfulfilledOrders int64
	CompletedOrders   int64
	PaidToman         int64
}

func (s Orders) Overview(ctx context.Context) (SalesOverview, error) {
	if s.DB == nil {
		return SalesOverview{}, fmt.Errorf("order store is unavailable")
	}
	var result SalesOverview
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN ('paid', 'provisioning') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status IN ('paid', 'provisioning', 'completed') THEN price_toman ELSE 0 END), 0)
		FROM shop_orders`).Scan(&result.TotalOrders, &result.PendingOrders, &result.UnfulfilledOrders, &result.CompletedOrders, &result.PaidToman)
	return result, err
}

// DebitedPending finds orders whose wallet journal committed just before the
// panel stopped, leaving the order status at pending. The journal reference,
// owner, amount, and reason must all match the immutable order snapshot.
func (s Orders) DebitedPending(ctx context.Context, limit int, dialect string) ([]DebitedOrder, error) {
	if s.DB == nil || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("invalid order recovery limit")
	}
	match := `e.reference = ('shop-order:' || o.id)`
	if strings.EqualFold(dialect, "mysql") {
		match = `e.reference = CONCAT('shop-order:', o.id)`
	}
	query := `SELECT o.id, o.telegram_id FROM shop_orders o JOIN shop_wallet_entries e ON ` + match + `
		WHERE o.status = 'pending' AND e.telegram_id = o.telegram_id AND e.delta = -o.price_toman AND e.reason = 'purchase'
		ORDER BY o.updated_at, o.id LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []DebitedOrder{}
	for rows.Next() {
		var item DebitedOrder
		if err := rows.Scan(&item.ID, &item.TelegramID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Unfulfilled returns only orders whose wallet debit has already been recorded
// and whose account delivery can be safely retried after a process restart.
func (s Orders) Unfulfilled(ctx context.Context, limit int) ([]string, error) {
	if s.DB == nil || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("invalid order recovery limit")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM shop_orders WHERE status IN ('paid', 'provisioning') ORDER BY updated_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s Orders) ListCustomerOrders(ctx context.Context, telegramID int64) ([]Order, error) {
	if s.DB == nil || telegramID <= 0 {
		return nil, fmt.Errorf("invalid customer")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE telegram_id = ? ORDER BY created_at DESC LIMIT 20`, telegramID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []Order{}
	for rows.Next() {
		var order Order
		if err := rows.Scan(&order.ID, &order.RequestKey, &order.TelegramID, &order.ProductID, &order.ServiceID, &order.PriceToman, &order.DataLimitBytes, &order.DurationDays, &order.Status, &order.Username); err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

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
	order, err := scanOrder(tx.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE id = ?`, id))
	if err != nil {
		return Order{}, err
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s Orders) byKey(ctx context.Context, key string) (Order, bool, error) {
	order, err := scanOrder(s.DB.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE request_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	return order, err == nil, err
}

func scanOrder(row *sql.Row) (Order, error) {
	var order Order
	err := row.Scan(&order.ID, &order.RequestKey, &order.TelegramID, &order.ProductID, &order.ServiceID, &order.PriceToman, &order.DataLimitBytes, &order.DurationDays, &order.Status, &order.Username)
	return order, err
}

// Pay debits the wallet once for this order. A crash after the debit and
// before the status update is recoverable by retrying with the same reference.
func (s Orders) Pay(ctx context.Context, telegramID int64, orderID string, wallet Wallet) (Order, error) {
	if s.DB == nil || wallet.DB != s.DB || telegramID <= 0 || orderID == "" {
		return Order{}, fmt.Errorf("invalid order payment request")
	}
	order, err := scanOrder(s.DB.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE id = ?`, orderID))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrOrderUnavailable
	}
	if err != nil {
		return Order{}, err
	}
	if order.TelegramID != telegramID {
		return Order{}, ErrOrderUnavailable
	}
	if order.Status == "paid" || order.Status == "provisioning" || order.Status == "completed" {
		return order, nil
	}
	if order.Status != "pending" {
		return Order{}, ErrOrderUnavailable
	}
	if _, err := wallet.Debit(ctx, telegramID, order.PriceToman, "shop-order:"+order.ID); err != nil {
		return Order{}, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE shop_orders SET status = 'paid', updated_at = ? WHERE id = ? AND status = 'pending'`, time.Now().UTC().Format("2006-01-02 15:04:05"), orderID)
	if err != nil {
		return Order{}, err
	}
	order, err = scanOrder(s.DB.QueryRowContext(ctx, `SELECT id, request_key, telegram_id, product_id, service_id, price_toman, data_limit_bytes, duration_days, status, username FROM shop_orders WHERE id = ?`, orderID))
	if err != nil {
		return Order{}, err
	}
	if order.Status != "paid" && order.Status != "provisioning" && order.Status != "completed" {
		return Order{}, ErrOrderUnavailable
	}
	return order, nil
}
