package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000083_telegram_shop_topups.go", up000083TelegramShopTopups, emptyDown)
}

func up000083TelegramShopTopups(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "shop_topup_requests", `
CREATE TABLE shop_topup_requests (
	id VARCHAR(36) PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	amount_toman BIGINT NOT NULL CHECK (amount_toman > 0),
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	receipt_file_id VARCHAR(512) NULL,
	reviewed_by BIGINT NULL,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`, `
CREATE TABLE shop_topup_requests (
	id VARCHAR(36) NOT NULL PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	amount_toman BIGINT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	receipt_file_id VARCHAR(512) NULL,
	reviewed_by BIGINT NULL,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	CONSTRAINT shop_topup_amount_positive CHECK (amount_toman > 0)
)`); err != nil {
		return err
	}
	if err := createIndex(ctx, tx, dialect, "shop_topup_requests", "ix_shop_topup_owner_status", []string{"telegram_id", "status"}, false); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "shop_payment_settings", `
CREATE TABLE shop_payment_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	instructions TEXT NOT NULL
)`, `
CREATE TABLE shop_payment_settings (
	id INTEGER NOT NULL PRIMARY KEY,
	instructions TEXT NOT NULL,
	CONSTRAINT shop_payment_settings_one CHECK (id = 1)
)`)
}
