package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000093_api_key_scope.go", up000093APIKeyScope, emptyDown)
}

func up000093APIKeyScope(ctx context.Context, tx *sql.Tx) error {
	hasTable, err := HasTable(ctx, tx, activeDialect(), "admin_api_keys")
	if err != nil || !hasTable {
		return err
	}
	return addColumn(ctx, tx, activeDialect(), "admin_api_keys", "scope",
		"TEXT NOT NULL DEFAULT 'full'", "VARCHAR(32) NOT NULL DEFAULT 'full'")
}
