package migrations

import (
	"context"
	"database/sql"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000067_operation_executor_leases.go", up000067OperationExecutorLeases, emptyDown)
}

func up000067OperationExecutorLeases(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "operation_executor_leases", operationapp.ExecutorLeaseDDL, operationapp.ExecutorLeaseDDL)
}
