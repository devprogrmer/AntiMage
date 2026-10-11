package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000065_operations.go", up000065Operations, emptyDown)
}

func up000065Operations(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "operations", `
CREATE TABLE operations (
	id VARCHAR(96) PRIMARY KEY,
	operation_type VARCHAR(48) NOT NULL,
	target_type VARCHAR(48) NOT NULL,
	target_id VARCHAR(128) NOT NULL,
	requested_by VARCHAR(128) NOT NULL,
	request_id VARCHAR(96) NOT NULL,
	state VARCHAR(16) NOT NULL,
	phase VARCHAR(48) NOT NULL,
	progress INTEGER NULL,
	created_at BIGINT NOT NULL,
	started_at BIGINT NULL,
	updated_at BIGINT NOT NULL,
	completed_at BIGINT NULL,
	error TEXT NOT NULL,
	metadata_json TEXT NOT NULL
)
`, `
CREATE TABLE operations (
	id VARCHAR(96) NOT NULL,
	operation_type VARCHAR(48) NOT NULL,
	target_type VARCHAR(48) NOT NULL,
	target_id VARCHAR(128) NOT NULL,
	requested_by VARCHAR(128) NOT NULL,
	request_id VARCHAR(96) NOT NULL,
	state VARCHAR(16) NOT NULL,
	phase VARCHAR(48) NOT NULL,
	progress INTEGER NULL,
	created_at BIGINT NOT NULL,
	started_at BIGINT NULL,
	updated_at BIGINT NOT NULL,
	completed_at BIGINT NULL,
	error TEXT NOT NULL,
	metadata_json LONGTEXT NOT NULL,
	PRIMARY KEY (id)
)
`); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "operations", "ix_operations_state_updated", []string{"state", "updated_at"}, false)
}
