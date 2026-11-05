package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000085_telegram_shop_recovery_index.go", up000085TelegramShopRecoveryIndex, emptyDown)
}

func up000085TelegramShopRecoveryIndex(ctx context.Context, tx *sql.Tx) error {
	return createIndex(ctx, tx, activeDialect(), "shop_orders", "ix_shop_orders_recovery", []string{"status", "updated_at", "id"}, false)
}
