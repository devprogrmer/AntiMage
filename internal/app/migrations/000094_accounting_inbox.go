package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000094_accounting_inbox.go", up000094AccountingInbox, emptyDown)
}

func up000094AccountingInbox(ctx context.Context, tx *sql.Tx) error {
	const schema = `CREATE TABLE node_accounting_alerts (
		node_id BIGINT NOT NULL,
		protocol VARCHAR(64) NOT NULL,
		kind VARCHAR(64) NOT NULL,
		node_name VARCHAR(255) NOT NULL,
		opened_at BIGINT NOT NULL,
		resolved_at BIGINT NULL,
		read_at BIGINT NULL,
		PRIMARY KEY (node_id, protocol, kind)
	)`
	return createTable(ctx, tx, activeDialect(), "node_accounting_alerts", schema, schema)
}
