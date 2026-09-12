package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext(
		"000054_wireguard_usage_reflection.go",
		up000054WireGuardUsageReflection,
		emptyDown,
	)
}

func up000054WireGuardUsageReflection(
	ctx context.Context,
	tx *sql.Tx,
) error {
	dialect := activeDialect()

	return createTable(
		ctx,
		tx,
		dialect,
		"node_wireguard_usage_reflection",
		`
CREATE TABLE node_wireguard_usage_reflection (
node_id INTEGER NOT NULL,
user_id INTEGER NOT NULL,
batch_id TEXT NOT NULL,
updated_at DATETIME NOT NULL,
PRIMARY KEY (node_id, user_id)
)`,
		`
CREATE TABLE node_wireguard_usage_reflection (
node_id BIGINT NOT NULL,
user_id BIGINT NOT NULL,
batch_id VARCHAR(255) NOT NULL,
updated_at DATETIME(6) NOT NULL,
PRIMARY KEY (node_id, user_id)
)`,
	)
}
