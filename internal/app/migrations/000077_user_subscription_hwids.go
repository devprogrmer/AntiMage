package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000077_user_subscription_hwids.go", up000077UserSubscriptionHWIDs, emptyDown)
}

func up000077UserSubscriptionHWIDs(ctx context.Context, tx *sql.Tx) error {
	return createTable(ctx, tx, activeDialect(), "user_subscription_hwids", `
CREATE TABLE user_subscription_hwids (
	user_id INTEGER NOT NULL,
	hwid_hash TEXT NOT NULL,
	first_used_at DATETIME NOT NULL,
	last_used_at DATETIME NOT NULL,
	PRIMARY KEY (user_id, hwid_hash)
)`, `
CREATE TABLE user_subscription_hwids (
	user_id BIGINT NOT NULL,
	hwid_hash CHAR(64) NOT NULL,
	first_used_at DATETIME NOT NULL,
	last_used_at DATETIME NOT NULL,
	PRIMARY KEY (user_id, hwid_hash)
)`)
}
