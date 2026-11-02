package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000080_telegram_shop_orders.go", up000080TelegramShopOrders, emptyDown)
}

func up000080TelegramShopOrders(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "shop_products", `
CREATE TABLE shop_products (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	service_id BIGINT NOT NULL,
	title VARCHAR(160) NOT NULL,
	price_toman BIGINT NOT NULL CHECK (price_toman > 0),
	data_limit_bytes BIGINT NOT NULL CHECK (data_limit_bytes > 0),
	duration_days INTEGER NOT NULL CHECK (duration_days > 0),
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL
)`, `
CREATE TABLE shop_products (
	id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
	service_id BIGINT NOT NULL,
	title VARCHAR(160) NOT NULL,
	price_toman BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days INTEGER NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT 1,
	created_at DATETIME NOT NULL,
	CONSTRAINT shop_product_price_positive CHECK (price_toman > 0),
	CONSTRAINT shop_product_data_positive CHECK (data_limit_bytes > 0),
	CONSTRAINT shop_product_days_positive CHECK (duration_days > 0)
)`); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "shop_orders", `
CREATE TABLE shop_orders (
	id VARCHAR(36) PRIMARY KEY,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	telegram_id BIGINT NOT NULL,
	product_id BIGINT NOT NULL,
	service_id BIGINT NOT NULL,
	price_toman BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days INTEGER NOT NULL,
	status VARCHAR(32) NOT NULL DEFAULT 'pending',
	username VARCHAR(34) NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	FOREIGN KEY (product_id) REFERENCES shop_products(id)
)`, `
CREATE TABLE shop_orders (
	id VARCHAR(36) NOT NULL PRIMARY KEY,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	telegram_id BIGINT NOT NULL,
	product_id BIGINT NOT NULL,
	service_id BIGINT NOT NULL,
	price_toman BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days INTEGER NOT NULL,
	status VARCHAR(32) NOT NULL DEFAULT 'pending',
	username VARCHAR(34) NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	CONSTRAINT shop_orders_product FOREIGN KEY (product_id) REFERENCES shop_products(id)
)`)
}
