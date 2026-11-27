package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("000095_network_quality_history.go", up000095NetworkQualityHistory, emptyDown)
}

func up000095NetworkQualityHistory(ctx context.Context, tx *sql.Tx) error {
	const schema = `CREATE TABLE network_quality_history (
		node_id BIGINT NOT NULL,
		user_id BIGINT NOT NULL,
		observed_at BIGINT NOT NULL,
		ack_rtt_ms DOUBLE NULL,
		jitter_ms DOUBLE NULL,
		retransmission_delta BIGINT NULL,
		PRIMARY KEY (node_id, user_id, observed_at)
	)`
	if err := createTable(ctx, tx, activeDialect(), "network_quality_history", schema, schema); err != nil {
		return err
	}
	return createIndex(ctx, tx, activeDialect(), "network_quality_history", "ix_network_quality_history_time", []string{"observed_at"}, false)
}
