package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000069_structured_operation_events.go", up000069StructuredOperationEvents, emptyDown)
}

func up000069StructuredOperationEvents(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := addColumn(ctx, tx, dialect, "operation_events", "event_type", "VARCHAR(96) NOT NULL DEFAULT 'operation.transition'", "VARCHAR(96) NOT NULL DEFAULT 'operation.transition'"); err != nil {
		return err
	}
	if err := addColumn(ctx, tx, dialect, "operation_events", "payload_json", "TEXT NULL", "LONGTEXT NULL"); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "operation_events", "ix_operation_events_type_time", []string{"event_type", "observed_at"}, false)
}
