package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000060_user_subscription_message.go", up000060UserSubscriptionMessage, emptyDown)
}

func up000060UserSubscriptionMessage(ctx context.Context, tx *sql.Tx) error {
	_, err := AddColumnIfMissing(ctx, tx, activeDialect(), "users", "subscription_message", "TEXT")
	return err
}
