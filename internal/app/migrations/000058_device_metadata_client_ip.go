package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext(
		"000058_device_metadata_client_ip.go",
		up000058DeviceMetadataClientIP,
		emptyDown,
	)
}

func up000058DeviceMetadataClientIP(
	ctx context.Context,
	tx *sql.Tx,
) error {
	dialect := activeDialect()

	if _, err := AddColumnIfMissing(
		ctx,
		tx,
		dialect,
		"vpn_device_metadata",
		"client_ip",
		"VARCHAR(45) NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}

	_, err := CreateIndexIfMissing(
		ctx,
		tx,
		dialect,
		"vpn_device_metadata",
		"ix_vpn_device_metadata_client_ip",
		[]string{
			"user_id",
			"protocol",
			"client_ip",
		},
		false,
	)

	return err
}
