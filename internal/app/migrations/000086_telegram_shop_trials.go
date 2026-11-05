package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000086_telegram_shop_trials.go", up000086TelegramShopTrials, emptyDown)
}

func up000086TelegramShopTrials(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := createTable(ctx, tx, dialect, "shop_trial_config", `
CREATE TABLE shop_trial_config (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	service_id BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days BIGINT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1
)`, `
CREATE TABLE shop_trial_config (
	id INTEGER NOT NULL PRIMARY KEY,
	service_id BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days BIGINT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT 1,
	CONSTRAINT shop_trial_config_one CHECK (id = 1)
)`); err != nil {
		return err
	}
	if err := createTable(ctx, tx, dialect, "shop_trial_claims", `
CREATE TABLE shop_trial_claims (
	telegram_id BIGINT PRIMARY KEY,
	claim_id VARCHAR(36) NOT NULL UNIQUE,
	service_id BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days BIGINT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	username VARCHAR(34) NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`, `
CREATE TABLE shop_trial_claims (
	telegram_id BIGINT NOT NULL PRIMARY KEY,
	claim_id VARCHAR(36) NOT NULL UNIQUE,
	service_id BIGINT NOT NULL,
	data_limit_bytes BIGINT NOT NULL,
	duration_days BIGINT NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'pending',
	username VARCHAR(34) NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "shop_trial_claims", "ix_shop_trial_recovery", []string{"status", "updated_at", "telegram_id"}, false)
}
