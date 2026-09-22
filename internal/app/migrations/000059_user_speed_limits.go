package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext(
		"000059_user_speed_limits.go",
		up000059UserSpeedLimits,
		emptyDown,
	)
}

func up000059UserSpeedLimits(
	ctx context.Context,
	tx *sql.Tx,
) error {
	dialect := activeDialect()

	if _, err := AddColumnIfMissing(
		ctx,
		tx,
		dialect,
		"users",
		"upload_speed_limit",
		"BIGINT NOT NULL DEFAULT 0",
	); err != nil {
		return err
	}

	_, err := AddColumnIfMissing(
		ctx,
		tx,
		dialect,
		"users",
		"download_speed_limit",
		"BIGINT NOT NULL DEFAULT 0",
	)

	return err
}
