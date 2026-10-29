package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000076_inbound_speed_limits.go", up000076InboundSpeedLimits, emptyDown)
}

// Keep the version above the previous hardening migrations. Existing databases
// may already have these columns; their values must survive this migration.
func up000076InboundSpeedLimits(ctx context.Context, tx *sql.Tx) error {
	exists, err := HasTable(ctx, tx, activeDialect(), "inbounds")
	if err != nil || !exists {
		return err
	}
	for _, column := range []string{"upload_speed_limit", "download_speed_limit"} {
		if _, err := AddColumnIfMissing(ctx, tx, activeDialect(), "inbounds", column, "BIGINT NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}
