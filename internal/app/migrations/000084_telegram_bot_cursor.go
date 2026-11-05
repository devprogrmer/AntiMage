package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000084_telegram_bot_cursor.go", up000084TelegramBotCursor, emptyDown)
}

func up000084TelegramBotCursor(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "telegram_bot_cursor", `
CREATE TABLE telegram_bot_cursor (
	token_hash VARCHAR(64) PRIMARY KEY,
	next_update_id BIGINT NOT NULL
)`, `
CREATE TABLE telegram_bot_cursor (
	token_hash VARCHAR(64) NOT NULL PRIMARY KEY,
	next_update_id BIGINT NOT NULL
)`)
}
