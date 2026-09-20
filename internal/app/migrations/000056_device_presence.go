package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000056_device_presence.go", up000056DevicePresence, emptyDown)
}

func up000056DevicePresence(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if _, err := AddColumnIfMissing(ctx, tx, dialect, "users", "device_limit", "BIGINT NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{
		{"device_id", "VARCHAR(128) NULL"},
		{"device_type", "VARCHAR(32) NOT NULL DEFAULT 'Unknown'"},
		{"client_name", "VARCHAR(64) NOT NULL DEFAULT 'Unknown'"},
		{"platform", "VARCHAR(64) NOT NULL DEFAULT 'Unknown'"},
	} {
		if _, err := AddColumnIfMissing(ctx, tx, dialect, "vpn_user_sessions", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := createIndex(ctx, tx, dialect, "vpn_user_sessions", "ix_vpn_user_sessions_device", []string{"user_id", "protocol", "device_id"}, false); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "wireguard_devices", `
CREATE TABLE wireguard_devices (
inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL,
private_key TEXT NOT NULL, public_key TEXT NOT NULL, address TEXT NOT NULL,
generation INTEGER NOT NULL DEFAULT 1,
PRIMARY KEY (inbound_tag, user_id, device_index),
UNIQUE (inbound_tag, public_key), UNIQUE (inbound_tag, address)
)`, `
CREATE TABLE wireguard_devices (
inbound_tag VARCHAR(255) NOT NULL, user_id BIGINT NOT NULL, device_index INT NOT NULL,
private_key VARCHAR(64) NOT NULL, public_key VARCHAR(64) NOT NULL, address VARCHAR(45) NOT NULL,
generation INT NOT NULL DEFAULT 1,
PRIMARY KEY (inbound_tag, user_id, device_index),
UNIQUE KEY uq_wireguard_device_public_key (inbound_tag, public_key),
UNIQUE KEY uq_wireguard_device_address (inbound_tag, address)
)`)
}
