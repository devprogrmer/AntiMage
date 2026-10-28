package migrations

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000072_usage_reflection_ledger.go", up000072UsageReflectionLedger, emptyDown)
}
func up000072UsageReflectionLedger(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	for _, table := range []string{"node_usage_user_queue", "node_wireguard_usage_reflection"} {
		if err := addColumn(ctx, tx, dialect, table, "ledger_json", "TEXT NULL", "MEDIUMTEXT NULL"); err != nil {
			return err
		}
	}
	return createTable(ctx, tx, dialect, "node_usage_reflection_staged", `CREATE TABLE node_usage_reflection_staged (
 node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,epoch VARCHAR(64) NOT NULL,owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,total_raw BIGINT NOT NULL,
 PRIMARY KEY(node_id,user_id,epoch,owner_hash))`, `CREATE TABLE node_usage_reflection_staged (
 node_id BIGINT NOT NULL,user_id BIGINT NOT NULL,epoch VARCHAR(64) NOT NULL,owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,total_raw BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(node_id,user_id,epoch,owner_hash))`)
}
