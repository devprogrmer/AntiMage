package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext(
		"000057_vpn_device_metadata.go",
		up000057VPNDeviceMetadata,
		emptyDown,
	)
}

func up000057VPNDeviceMetadata(ctx context.Context, tx *sql.Tx) error {
	return createTable(
		ctx,
		tx,
		activeDialect(),
		"vpn_device_metadata",
		`
CREATE TABLE vpn_device_metadata (
	user_id INTEGER NOT NULL,
	protocol TEXT NOT NULL,
	inbound_tag TEXT NOT NULL DEFAULT '',
	device_id TEXT NOT NULL,
	device_type TEXT NOT NULL DEFAULT 'Unknown',
	manufacturer TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	os_name TEXT NOT NULL DEFAULT '',
	os_version TEXT NOT NULL DEFAULT '',
	client_name TEXT NOT NULL DEFAULT 'Unknown',
	client_version TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT 'Unknown',
	metadata_source TEXT NOT NULL DEFAULT 'subscription',
	last_seen_at DATETIME NOT NULL,
	PRIMARY KEY (user_id, protocol, inbound_tag, device_id)
)`,
		`
CREATE TABLE vpn_device_metadata (
	user_id BIGINT NOT NULL,
	protocol VARCHAR(32) NOT NULL,
	inbound_tag VARCHAR(255) NOT NULL DEFAULT '',
	device_id VARCHAR(128) NOT NULL,
	device_type VARCHAR(32) NOT NULL DEFAULT 'Unknown',
	manufacturer VARCHAR(128) NOT NULL DEFAULT '',
	model VARCHAR(128) NOT NULL DEFAULT '',
	os_name VARCHAR(64) NOT NULL DEFAULT '',
	os_version VARCHAR(64) NOT NULL DEFAULT '',
	client_name VARCHAR(64) NOT NULL DEFAULT 'Unknown',
	client_version VARCHAR(64) NOT NULL DEFAULT '',
	platform VARCHAR(128) NOT NULL DEFAULT 'Unknown',
	metadata_source VARCHAR(32) NOT NULL DEFAULT 'subscription',
	last_seen_at DATETIME NOT NULL,
	PRIMARY KEY (user_id, protocol, inbound_tag, device_id)
)`,
	)
}
