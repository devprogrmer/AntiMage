package migrations

import (
	"context"
	"database/sql"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000068_operation_resource_fences.go", up000068OperationResourceFences, emptyDown)
}
func up000068OperationResourceFences(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "operation_resource_fences", operationapp.ResourceFenceDDL, operationapp.ResourceFenceDDL)
}
