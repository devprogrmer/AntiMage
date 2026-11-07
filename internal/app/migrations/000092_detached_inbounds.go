package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000092_detached_inbounds.go", up000092DetachedInbounds, emptyDown)
}

func up000092DetachedInbounds(ctx context.Context, tx *sql.Tx) error {
	const schema = `CREATE TABLE detached_inbounds (tag VARCHAR(255) PRIMARY KEY, config LONGTEXT NOT NULL)`
	return createTable(ctx, tx, activeDialect(), "detached_inbounds", schema, schema)
}
