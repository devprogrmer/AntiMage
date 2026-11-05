package shop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s Orders) CreateProduct(ctx context.Context, product Product, requestKey string) (Product, error) {
	product.Title = strings.TrimSpace(product.Title)
	if s.DB == nil || product.ServiceID <= 0 || product.Title == "" || len(product.Title) > 160 ||
		product.PriceToman <= 0 || product.DataLimitBytes <= 0 || product.DurationDays <= 0 ||
		requestKey == "" || len(requestKey) > 128 || strings.TrimSpace(requestKey) != requestKey {
		return Product{}, fmt.Errorf("invalid shop product")
	}
	if existing, found, err := s.productByKey(ctx, requestKey); err != nil {
		return Product{}, err
	} else if found {
		if !sameProductSpec(existing, product) {
			return Product{}, ErrReferenceConflict
		}
		return existing, nil
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO shop_products (service_id, title, price_toman, data_limit_bytes, duration_days, enabled, created_at, request_key)
		SELECT id, ?, ?, ?, ?, 1, ?, ? FROM services WHERE id = ?`, product.Title, product.PriceToman, product.DataLimitBytes, product.DurationDays, time.Now().UTC().Format("2006-01-02 15:04:05"), requestKey, product.ServiceID)
	if err != nil {
		if existing, found, lookupErr := s.productByKey(ctx, requestKey); lookupErr == nil && found {
			if !sameProductSpec(existing, product) {
				return Product{}, ErrReferenceConflict
			}
			return existing, nil
		}
		return Product{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Product{}, err
	}
	if affected != 1 {
		return Product{}, ErrProductUnavailable
	}
	created, found, err := s.productByKey(ctx, requestKey)
	if err != nil {
		return Product{}, err
	}
	if !found {
		return Product{}, fmt.Errorf("created product not found")
	}
	return created, nil
}

func (s Orders) DisableProduct(ctx context.Context, productID int64) error {
	if s.DB == nil || productID <= 0 {
		return fmt.Errorf("invalid product ID")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE shop_products SET enabled = 0 WHERE id = ?`, productID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var id int64
		if err := s.DB.QueryRowContext(ctx, `SELECT id FROM shop_products WHERE id = ?`, productID).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return ErrProductUnavailable
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s Orders) productByKey(ctx context.Context, requestKey string) (Product, bool, error) {
	var product Product
	err := s.DB.QueryRowContext(ctx, `SELECT id, service_id, title, price_toman, data_limit_bytes, duration_days, enabled FROM shop_products WHERE request_key = ?`, requestKey).
		Scan(&product.ID, &product.ServiceID, &product.Title, &product.PriceToman, &product.DataLimitBytes, &product.DurationDays, &product.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, false, nil
	}
	return product, err == nil, err
}

func sameProductSpec(a, b Product) bool {
	return a.ServiceID == b.ServiceID && a.Title == b.Title && a.PriceToman == b.PriceToman &&
		a.DataLimitBytes == b.DataLimitBytes && a.DurationDays == b.DurationDays
}
