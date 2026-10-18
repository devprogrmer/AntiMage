package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000074_usage_retirement_proofs.go", up000074UsageRetirementProofs, emptyDown)
}

func up000074UsageRetirementProofs(ctx context.Context, tx *sql.Tx) error {
	dialect := activeDialect()
	if err := addColumn(ctx, tx, dialect, "node_usage_reflection_staged", "legacy_raw", "BIGINT NOT NULL DEFAULT 0", "BIGINT UNSIGNED NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn(ctx, tx, dialect, "node_usage_reflection_staged", "legacy_raw_complete", "BOOLEAN NOT NULL DEFAULT 0", "BOOLEAN NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return createTable(ctx, tx, dialect, "node_usage_retirement_proofs", `CREATE TABLE node_usage_retirement_proofs (
 proof_id CHAR(64) NOT NULL PRIMARY KEY,node_id INTEGER NOT NULL,user_id INTEGER NOT NULL,epoch VARCHAR(64) NOT NULL,
 owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,total_raw BIGINT NOT NULL,legacy_raw BIGINT NOT NULL,
 created_at DATETIME NOT NULL,delivered_at DATETIME NULL,UNIQUE(node_id,user_id,epoch,owner_hash))`, `CREATE TABLE node_usage_retirement_proofs (
 proof_id CHAR(64) NOT NULL PRIMARY KEY,node_id BIGINT NOT NULL,user_id BIGINT NOT NULL,epoch VARCHAR(64) NOT NULL,
 owner_hash CHAR(64) NOT NULL,owner_key TEXT NOT NULL,total_raw BIGINT UNSIGNED NOT NULL,legacy_raw BIGINT UNSIGNED NOT NULL,
 created_at DATETIME NOT NULL,delivered_at DATETIME NULL,UNIQUE KEY node_usage_retirement_owner_uq(node_id,user_id,epoch,owner_hash),
 KEY node_usage_retirement_pending_idx(node_id,delivered_at))`)
}
