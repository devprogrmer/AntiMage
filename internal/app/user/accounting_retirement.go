package user

import (
	"context"
	"database/sql"
	"time"

	"github.com/antimage/antimage/internal/platform/usagereflection"
)

// RecordAccountingDeletionTx must be called in the transaction that commits a
// user's soft-delete. It creates a durable accounting tombstone and marks any
// pending usage rows as retirement work rather than silently dropping them.
func RecordAccountingDeletionTx(ctx context.Context, tx *sql.Tx, dialect string, userIDs []int64, deletedAt time.Time) error {
	return usagereflection.RecordUserDeletionTx(ctx, tx, dialect, userIDs, deletedAt)
}

func (r Repository) recordAccountingDeletionTx(ctx context.Context, tx *sql.Tx, userIDs []int64, deletedAt time.Time) error {
	return RecordAccountingDeletionTx(ctx, tx, r.dialect, userIDs, deletedAt)
}
