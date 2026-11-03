package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000088_telegram_bot_language.go", up000088TelegramBotLanguage, emptyDown)
}

func up000088TelegramBotLanguage(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "telegram_bot_languages", `
CREATE TABLE telegram_bot_languages (
	telegram_id BIGINT PRIMARY KEY,
	language VARCHAR(2) NOT NULL
)`, `
CREATE TABLE telegram_bot_languages (
	telegram_id BIGINT NOT NULL PRIMARY KEY,
	language VARCHAR(2) NOT NULL
)`)
}
