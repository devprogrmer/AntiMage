package migrations

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000090_telegram_order_changes.go", up000090TelegramOrderChanges, emptyDown)
}

func up000090TelegramOrderChanges(ctx context.Context, tx *sql.Tx) error {
	const schema = `CREATE TABLE shop_order_changes (
		order_id VARCHAR(36) PRIMARY KEY,
		kind VARCHAR(16) NOT NULL CHECK (kind IN ('renew','volume')),
		username VARCHAR(34) NOT NULL,
		FOREIGN KEY (order_id) REFERENCES shop_orders(id)
	)`
	return createTable(ctx, tx, activeDialect(), "shop_order_changes", schema, schema)
}
