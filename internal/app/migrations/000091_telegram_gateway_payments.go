package migrations

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000091_telegram_gateway_payments.go", up000091TelegramGatewayPayments, emptyDown)
}
func up000091TelegramGatewayPayments(ctx context.Context, tx *sql.Tx) error {
	const schema = `CREATE TABLE shop_gateway_payments (
		id VARCHAR(36) PRIMARY KEY, request_key VARCHAR(128) NOT NULL UNIQUE,
		telegram_id BIGINT NOT NULL, amount_toman BIGINT NOT NULL CHECK(amount_toman>0),
		merchant_id VARCHAR(36) NOT NULL, sandbox BOOLEAN NOT NULL,
		status VARCHAR(16) NOT NULL, authority VARCHAR(128) UNIQUE, reference VARCHAR(128) UNIQUE,
		lease_until BIGINT NOT NULL DEFAULT 0,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL)`
	if err := createTable(ctx, tx, activeDialect(), "shop_gateway_payments", schema, schema); err != nil {
		return err
	}
	return createIndex(ctx, tx, activeDialect(), "shop_gateway_payments", "ix_shop_gateway_status_created", []string{"status", "created_at"}, false)
}
