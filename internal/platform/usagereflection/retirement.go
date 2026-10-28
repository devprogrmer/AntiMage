package usagereflection

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RecordUserDeletionTx records a durable soft-delete tombstone and marks any
// already-staged usage receipts for retirement. Call it in the same SQL
// transaction that changes users.status to deleted. The queue rows remain the
// durable raw-watermark journal until they are consolidated into the bounded
// per-owner retirement ledger by the usage flusher.
func RecordUserDeletionTx(ctx context.Context, tx *sql.Tx, dialect string, userIDs []int64, deletedAt time.Time) error {
	if tx == nil {
		return fmt.Errorf("accounting tombstone transaction is required")
	}
	ids := uniquePositiveUserIDs(userIDs)
	if len(ids) == 0 {
		return nil
	}
	if deletedAt.IsZero() {
		deletedAt = time.Now().UTC()
	}
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		var b strings.Builder
		b.WriteString(`INSERT `)
		if isMySQLDialect(dialect) {
			b.WriteString(`IGNORE `)
		}
		b.WriteString(`INTO user_accounting_tombstones(user_id,deleted_at) VALUES `)
		args := make([]any, 0, len(chunk)*2)
		for i, uid := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?)`)
			args = append(args, uid, deletedAt.UTC())
		}
		if !isMySQLDialect(dialect) {
			b.WriteString(` ON CONFLICT(user_id) DO NOTHING`)
		}
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("record user accounting tombstone: %w", err)
		}
	}
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		marks := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, uid := range chunk {
			marks[i] = `?`
			args[i] = uid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE node_usage_user_queue SET retirement_pending=1 WHERE processed_at IS NULL AND user_id IN (`+strings.Join(marks, `,`)+`)`, args...); err != nil {
			return fmt.Errorf("mark staged usage for retirement: %w", err)
		}
	}
	return nil
}

func uniquePositiveUserIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	ids := make([]int64, 0, len(values))
	for _, id := range values {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func isMySQLDialect(dialect string) bool {
	d := strings.ToLower(strings.TrimSpace(dialect))
	return strings.Contains(d, "mysql") || strings.Contains(d, "maria")
}
