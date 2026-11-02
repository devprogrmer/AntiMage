package shop

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProductCreateIdempotentAndDisabled(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE services (id BIGINT PRIMARY KEY)`,
		`INSERT INTO services VALUES (42)`,
		`CREATE TABLE shop_products (id INTEGER PRIMARY KEY AUTOINCREMENT, service_id BIGINT, title TEXT, price_toman BIGINT, data_limit_bytes BIGINT, duration_days BIGINT, enabled INTEGER, created_at TEXT, request_key TEXT UNIQUE)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := Orders{DB: db}
	spec := Product{ServiceID: 42, Title: "Basic", PriceToman: 80000, DataLimitBytes: 1 << 30, DurationDays: 30}
	first, err := store.CreateProduct(ctx, spec, "telegram-message-1")
	if err != nil || first.ID == 0 {
		t.Fatalf("create = %+v, %v", first, err)
	}
	second, err := store.CreateProduct(ctx, spec, "telegram-message-1")
	if err != nil || first.ID != second.ID {
		t.Fatalf("duplicate = %+v, %v", second, err)
	}
	spec.PriceToman = 90000
	if _, err := store.CreateProduct(ctx, spec, "telegram-message-1"); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	if err := store.DisableProduct(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableProduct(ctx, first.ID); err != nil {
		t.Fatalf("disable retry: %v", err)
	}
	if products, err := store.ListProducts(ctx); err != nil || len(products) != 0 {
		t.Fatalf("disabled plan visible: %+v, %v", products, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shop_products`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("product count = %d, %v", count, err)
	}
}
