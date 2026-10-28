package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000073_usage_retirement_state.go", up000073UsageRetirementState, emptyDown)
}

func up000073UsageRetirementState(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "user_accounting_tombstones", `CREATE TABLE user_accounting_tombstones (
 user_id INTEGER PRIMARY KEY, deleted_at DATETIME NOT NULL
)`, `CREATE TABLE user_accounting_tombstones (
 user_id BIGINT NOT NULL PRIMARY KEY, deleted_at DATETIME NOT NULL
)`); err != nil {
		return err
	}
	if err := addColumn(ctx, tx, dialect, "node_usage_user_queue", "retirement_pending", "BOOLEAN NOT NULL DEFAULT 0", "BOOLEAN NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "node_usage_retirement_ledger", `CREATE TABLE node_usage_retirement_ledger (
 node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,epoch VARCHAR(64) NOT NULL,owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,
 settled_raw BIGINT NOT NULL,total_raw BIGINT NOT NULL,retired_raw BIGINT NOT NULL,updated_at DATETIME NOT NULL,
 PRIMARY KEY(node_id,user_id,epoch,owner_hash))`, `CREATE TABLE node_usage_retirement_ledger (
 node_id BIGINT NOT NULL,user_id BIGINT NOT NULL,epoch VARCHAR(64) NOT NULL,owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,
 settled_raw BIGINT UNSIGNED NOT NULL,total_raw BIGINT UNSIGNED NOT NULL,retired_raw BIGINT UNSIGNED NOT NULL,updated_at DATETIME NOT NULL,
 PRIMARY KEY(node_id,user_id,epoch,owner_hash))`)
}
