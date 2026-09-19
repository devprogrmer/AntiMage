package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000055_amneziawg_devices.go", up000055AmneziaWGDevices, emptyDown)
}

func up000055AmneziaWGDevices(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "amneziawg_devices", `
CREATE TABLE amneziawg_devices (
inbound_tag TEXT NOT NULL,
user_id INTEGER NOT NULL,
device_index INTEGER NOT NULL,
private_key TEXT NOT NULL,
public_key TEXT NOT NULL,
preshared_key TEXT NOT NULL DEFAULT '',
address TEXT NOT NULL,
generation INTEGER NOT NULL DEFAULT 1,
PRIMARY KEY (inbound_tag, user_id, device_index),
UNIQUE (inbound_tag, public_key),
UNIQUE (inbound_tag, address)
)`, `
CREATE TABLE amneziawg_devices (
inbound_tag VARCHAR(255) NOT NULL,
user_id BIGINT NOT NULL,
device_index INT NOT NULL,
private_key VARCHAR(64) NOT NULL,
public_key VARCHAR(64) NOT NULL,
preshared_key VARCHAR(64) NOT NULL DEFAULT '',
address VARCHAR(45) NOT NULL,
generation INT NOT NULL DEFAULT 1,
PRIMARY KEY (inbound_tag, user_id, device_index),
UNIQUE KEY uq_amneziawg_public_key (inbound_tag, public_key),
UNIQUE KEY uq_amneziawg_address (inbound_tag, address)
)`)
}
