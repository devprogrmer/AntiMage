package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000087_telegram_wallet_history_index.go", up000087TelegramWalletHistoryIndex, emptyDown)
}

func up000087TelegramWalletHistoryIndex(ctx context.Context, tx *sql.Tx) error {
	return createIndex(ctx, tx, activeDialect(), "shop_wallet_entries", "ix_shop_wallet_history", []string{"telegram_id", "created_at", "reference"}, false)
}
