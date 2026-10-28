package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

// The inbound-level enforced speed cap is an independent per-user maximum on
// the inbound, not a shared aggregate budget: a cap of 8/8 lets every user on
// the inbound reach 8/8 at the same time.
//
// It lives in the same metadata columns as uplink/downlink/usage_coefficient
// because those already exist as structured inbound columns (rather than keys
// in the settings JSON blob), and it shares their lifecycle: created with the
// inbound, carried across settings edits, and never serialized into the Xray
// config the core reads. 0 means unlimited, matching users.upload_speed_limit.
func init() {
	goose.AddNamedMigrationContext(
		"000075_inbound_speed_limits.go",
		up000075InboundSpeedLimits,
		emptyDown,
	)
}

func up000075InboundSpeedLimits(
	ctx context.Context,
	tx *sql.Tx,
) error {
	dialect := activeDialect()

	exists, err := HasTable(ctx, tx, dialect, "inbounds")
	if err != nil || !exists {
		return err
	}

	if _, err := AddColumnIfMissing(
		ctx,
		tx,
		dialect,
		"inbounds",
		"upload_speed_limit",
		"BIGINT NOT NULL DEFAULT 0",
	); err != nil {
		return err
	}

	_, err = AddColumnIfMissing(
		ctx,
		tx,
		dialect,
		"inbounds",
		"download_speed_limit",
		"BIGINT NOT NULL DEFAULT 0",
	)

	return err
}
