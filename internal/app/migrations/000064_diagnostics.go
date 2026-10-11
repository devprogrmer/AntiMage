package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000064_diagnostics.go", up000064Diagnostics, emptyDown)
}

func up000064Diagnostics(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "diagnostics", `
CREATE TABLE diagnostics (
	id VARCHAR(96) PRIMARY KEY,
	source VARCHAR(48) NOT NULL,
	resource_type VARCHAR(48) NOT NULL,
	resource_id VARCHAR(128) NOT NULL,
	severity VARCHAR(16) NOT NULL,
	code VARCHAR(96) NOT NULL,
	summary TEXT NOT NULL,
	detail TEXT NOT NULL,
	first_seen_at BIGINT NOT NULL,
	last_seen_at BIGINT NOT NULL,
	occurrence_count BIGINT NOT NULL DEFAULT 1,
	status VARCHAR(16) NOT NULL,
	recommended_action TEXT NOT NULL
)
`, `
CREATE TABLE diagnostics (
	id VARCHAR(96) NOT NULL,
	source VARCHAR(48) NOT NULL,
	resource_type VARCHAR(48) NOT NULL,
	resource_id VARCHAR(128) NOT NULL,
	severity VARCHAR(16) NOT NULL,
	code VARCHAR(96) NOT NULL,
	summary TEXT NOT NULL,
	detail TEXT NOT NULL,
	first_seen_at BIGINT NOT NULL,
	last_seen_at BIGINT NOT NULL,
	occurrence_count BIGINT NOT NULL DEFAULT 1,
	status VARCHAR(16) NOT NULL,
	recommended_action TEXT NOT NULL,
	PRIMARY KEY (id)
)
`); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "diagnostics", "ix_diagnostics_status_severity_last_seen", []string{"status", "severity", "last_seen_at"}, false)
}
