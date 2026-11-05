package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000082_telegram_support.go", up000082TelegramSupport, emptyDown)
}

func up000082TelegramSupport(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "telegram_support_tickets", `
CREATE TABLE telegram_support_tickets (
	id VARCHAR(36) PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'open',
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL
)`, `
CREATE TABLE telegram_support_tickets (
	id VARCHAR(36) NOT NULL PRIMARY KEY,
	telegram_id BIGINT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'open',
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL
)`); err != nil {
		return err
	}
	if err := createIndex(ctx, tx, dialect, "telegram_support_tickets", "ix_telegram_support_owner_status", []string{"telegram_id", "status"}, false); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "telegram_support_messages", `
CREATE TABLE telegram_support_messages (
	id VARCHAR(36) PRIMARY KEY,
	ticket_id VARCHAR(36) NOT NULL,
	author VARCHAR(16) NOT NULL,
	content TEXT NOT NULL,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	created_at DATETIME(6) NOT NULL,
	FOREIGN KEY (ticket_id) REFERENCES telegram_support_tickets(id)
)`, `
CREATE TABLE telegram_support_messages (
	id VARCHAR(36) NOT NULL PRIMARY KEY,
	ticket_id VARCHAR(36) NOT NULL,
	author VARCHAR(16) NOT NULL,
	content TEXT NOT NULL,
	request_key VARCHAR(128) NOT NULL UNIQUE,
	created_at DATETIME(6) NOT NULL,
	CONSTRAINT telegram_support_messages_ticket FOREIGN KEY (ticket_id) REFERENCES telegram_support_tickets(id)
)`)
}
