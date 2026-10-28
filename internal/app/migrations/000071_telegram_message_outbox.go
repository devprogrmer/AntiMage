package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000071_telegram_message_outbox.go", up000071TelegramMessageOutbox, emptyDown)
}

func up000071TelegramMessageOutbox(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "telegram_message_outbox", `
CREATE TABLE telegram_message_outbox (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event VARCHAR(128) NOT NULL,
	message TEXT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	send_at DATETIME NOT NULL,
	claimed_at DATETIME NULL,
	last_error TEXT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)
`, `
CREATE TABLE telegram_message_outbox (
	id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
	event VARCHAR(128) NOT NULL,
	message TEXT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	attempts INT NOT NULL DEFAULT 0,
	send_at DATETIME(6) NOT NULL,
	claimed_at DATETIME(6) NULL,
	last_error TEXT NULL,
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL,
	KEY ix_telegram_message_outbox_due (status, send_at, id),
	KEY ix_telegram_message_outbox_claimed (status, claimed_at, id),
	KEY ix_telegram_message_outbox_retention (status, updated_at, id)
)
`); err != nil {
		return err
	}
	if NormalizeDialect(dialect) == "mysql" {
		return nil
	}
	if err := createIndex(ctx, tx, dialect, "telegram_message_outbox", "ix_telegram_message_outbox_due", []string{"status", "send_at", "id"}, false); err != nil {
		return err
	}
	if err := createIndex(ctx, tx, dialect, "telegram_message_outbox", "ix_telegram_message_outbox_claimed", []string{"status", "claimed_at", "id"}, false); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "telegram_message_outbox", "ix_telegram_message_outbox_retention", []string{"status", "updated_at", "id"}, false)
}
