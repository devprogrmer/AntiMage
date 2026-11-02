package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000079_telegram_shop_wallet.go", up000079TelegramShopWallet, emptyDown)
}

func up000079TelegramShopWallet(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "shop_wallet_accounts", `
CREATE TABLE shop_wallet_accounts (
	telegram_id BIGINT PRIMARY KEY,
	balance BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0)
)`, `
CREATE TABLE shop_wallet_accounts (
	telegram_id BIGINT NOT NULL PRIMARY KEY,
	balance BIGINT NOT NULL DEFAULT 0,
	CONSTRAINT shop_wallet_balance_nonnegative CHECK (balance >= 0)
)`); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "shop_wallet_entries", `
CREATE TABLE shop_wallet_entries (
	reference VARCHAR(128) PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	delta BIGINT NOT NULL CHECK (delta != 0),
	reason VARCHAR(32) NOT NULL,
	created_at DATETIME NOT NULL,
	FOREIGN KEY (telegram_id) REFERENCES shop_wallet_accounts(telegram_id)
)`, `
CREATE TABLE shop_wallet_entries (
	reference VARCHAR(128) NOT NULL PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	delta BIGINT NOT NULL,
	reason VARCHAR(32) NOT NULL,
	created_at DATETIME NOT NULL,
	CONSTRAINT shop_wallet_delta_nonzero CHECK (delta != 0),
	CONSTRAINT shop_wallet_entries_account FOREIGN KEY (telegram_id) REFERENCES shop_wallet_accounts(telegram_id)
)`)
}
