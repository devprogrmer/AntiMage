package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000089_telegram_miniapp_url.go", up000089TelegramMiniAppURL, emptyDown)
}

func up000089TelegramMiniAppURL(ctx context.Context, tx *sql.Tx) error {
	return addColumn(ctx, tx, activeDialect(), "telegram_settings", "mini_app_url", "TEXT NULL", "")
}
