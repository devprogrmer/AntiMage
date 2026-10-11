package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000070_wireguard_device_revocations.go", up000070WireGuardDeviceRevocations, emptyDown)
}

func up000070WireGuardDeviceRevocations(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "wireguard_device_revocations", `
CREATE TABLE wireguard_device_revocations (
	inbound_tag TEXT NOT NULL,
	user_id INTEGER NOT NULL,
	device_index INTEGER NOT NULL,
	public_key TEXT NOT NULL,
	credential_fingerprint VARCHAR(64) NOT NULL,
	revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (inbound_tag, user_id, device_index)
)
`, `
CREATE TABLE wireguard_device_revocations (
	inbound_tag VARCHAR(255) NOT NULL,
	user_id BIGINT NOT NULL,
	device_index INT NOT NULL,
	public_key VARCHAR(64) NOT NULL,
	credential_fingerprint CHAR(64) NOT NULL,
	revoked_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
	PRIMARY KEY (inbound_tag, user_id, device_index)
)
`)
}
