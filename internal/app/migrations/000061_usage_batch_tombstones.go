package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000061_usage_batch_tombstones.go", up000061UsageBatchTombstones, emptyDown)
}

func up000061UsageBatchTombstones(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "node_usage_batch_tombstones", `
CREATE TABLE node_usage_batch_tombstones (
node_id INTEGER NOT NULL,
batch_id TEXT NOT NULL,
kind TEXT NOT NULL,
pruned INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY (node_id, batch_id, kind)
)`, `
CREATE TABLE node_usage_batch_tombstones (
node_id BIGINT NOT NULL,
batch_id VARCHAR(255) NOT NULL,
kind VARCHAR(16) NOT NULL,
pruned TINYINT NOT NULL DEFAULT 0,
PRIMARY KEY (node_id, batch_id, kind)
)`)
}
