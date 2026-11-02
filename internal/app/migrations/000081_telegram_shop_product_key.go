package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000081_telegram_shop_product_key.go", up000081TelegramShopProductKey, emptyDown)
}

func up000081TelegramShopProductKey(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := addColumn(ctx, tx, dialect, "shop_products", "request_key", "VARCHAR(128) NULL", "VARCHAR(128) NULL"); err != nil {
		return err
	}
	return createIndex(ctx, tx, dialect, "shop_products", "uq_shop_products_request_key", []string{"request_key"}, true)
}
