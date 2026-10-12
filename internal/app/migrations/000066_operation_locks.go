package migrations

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000066_operation_locks.go", up000066OperationLocks, emptyDown)
}

func up000066OperationLocks(ctx context.Context, tx *sql.Tx) error {
	ddl := `CREATE TABLE operation_locks (
 target_type VARCHAR(48) NOT NULL,
 target_id VARCHAR(128) NOT NULL,
 operation_id VARCHAR(96) NOT NULL UNIQUE,
 PRIMARY KEY (target_type,target_id)
 )`
	if err := createTable(ctx, tx, activeDialect(), "operation_locks", ddl, ddl); err != nil {
		return err
	}
	ddl = `CREATE TABLE operation_events (
 operation_id VARCHAR(96) NOT NULL,
 sequence INTEGER NOT NULL,
 state VARCHAR(16) NOT NULL,
 phase VARCHAR(48) NOT NULL,
 observed_at BIGINT NOT NULL,
 requested_by VARCHAR(128) NOT NULL,
 request_id VARCHAR(96) NOT NULL,
 PRIMARY KEY(operation_id,sequence)
 )`
	return createTable(ctx, tx, activeDialect(), "operation_events", ddl, ddl)
}
