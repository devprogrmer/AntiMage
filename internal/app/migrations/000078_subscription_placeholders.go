package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000078_subscription_placeholders.go", up000078SubscriptionPlaceholders, emptyDown)
}

func up000078SubscriptionPlaceholders(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	for _, column := range []struct {
		name   string
		sqlite string
		mysql  string
	}{
		{"subscription_placeholder_enabled", "INTEGER NOT NULL DEFAULT 0", "BOOLEAN NOT NULL DEFAULT 0"},
		{"subscription_placeholder_expired_remark", "VARCHAR(255) NOT NULL DEFAULT 'Subscription expired'", ""},
		{"subscription_placeholder_limited_remark", "VARCHAR(255) NOT NULL DEFAULT 'Traffic limit reached'", ""},
		{"subscription_placeholder_disabled_remark", "VARCHAR(255) NOT NULL DEFAULT 'Subscription disabled'", ""},
	} {
		if err := addColumn(ctx, tx, dialect, "subscription_settings", column.name, column.sqlite, column.mysql); err != nil {
			return err
		}
	}
	return nil
}
