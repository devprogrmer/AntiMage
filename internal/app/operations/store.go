package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const columns = `id,operation_type,target_type,target_id,requested_by,request_id,state,phase,progress,created_at,started_at,updated_at,completed_at,error,metadata_json`

type Conflict struct {
	BlockingID string `json:"blocking_operation_id"`
}

func (e Conflict) Error() string { return "target is locked by operation " + e.BlockingID }

func Get(ctx context.Context, db *sql.DB, id string) (Operation, error) {
	return scan(db.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, id))
}

func ListTarget(ctx context.Context, db *sql.DB, targetType, targetID string, limit int) ([]Operation, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM operations WHERE target_type=? AND target_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, targetType, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Operation, 0)
	for rows.Next() {
		op, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Operation, error) {
	var op Operation
	var progress, started, completed sql.NullInt64
	var metadata string
	if err := row.Scan(&op.ID, &op.Type, &op.TargetType, &op.TargetID, &op.RequestedBy, &op.RequestID, &op.State, &op.Phase, &progress, &op.CreatedAt, &started, &op.UpdatedAt, &completed, &op.Error, &metadata); err != nil {
		return op, err
	}
	if progress.Valid {
		v := int(progress.Int64)
		op.Progress = &v
	}
	if started.Valid {
		v := started.Int64
		op.StartedAt = &v
	}
	if completed.Valid {
		v := completed.Int64
		op.CompletedAt = &v
	}
	decoder := json.NewDecoder(strings.NewReader(metadata))
	decoder.UseNumber()
	if err := decoder.Decode(&op.Metadata); err != nil {
		return op, fmt.Errorf("decode operation %s: %w", op.ID, err)
	}
	return op, nil
}

func Terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled" || state == "rolled_back"
}

// Active operations must not disappear behind a history limit during startup.
func ListActive(ctx context.Context, db *sql.DB) ([]Operation, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM operations WHERE state IN ('queued','running','waiting') ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Operation, 0)
	for rows.Next() {
		op, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

type RepairedLock struct {
	TargetType  string
	TargetID    string
	OperationID string
}

// RepairOrphanLocks records the repair before releasing a reservation. Both
// happen in the same transaction; a repeated startup cannot duplicate the event.
func RepairOrphanLocks(ctx context.Context, db *sql.DB) ([]RepairedLock, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT l.target_type,l.target_id,l.operation_id FROM operation_locks l LEFT JOIN operations o ON o.id=l.operation_id WHERE o.id IS NULL OR o.state IN ('completed','failed','cancelled','rolled_back') OR o.target_type<>l.target_type OR o.target_id<>l.target_id`)
	if err != nil {
		return nil, err
	}
	repairs := make([]RepairedLock, 0)
	for rows.Next() {
		var lock RepairedLock
		if err := rows.Scan(&lock.TargetType, &lock.TargetID, &lock.OperationID); err != nil {
			rows.Close()
			return nil, err
		}
		repairs = append(repairs, lock)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	confirmed := make([]RepairedLock, 0, len(repairs))
	for _, lock := range repairs {
		event := Operation{ID: lock.OperationID, TargetType: lock.TargetType, TargetID: lock.TargetID, State: "repaired", Phase: "orphan_lock_repaired", UpdatedAt: time.Now().UTC().Unix(), Metadata: map[string]any{"origin": "recovery"}}
		owner, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, lock.OperationID))
		if err == nil {
			event.RequestedBy, event.RequestID = owner.RequestedBy, owner.RequestID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// Recheck ownership under the write transaction; an active, correctly
		// targeted operation must never lose its reservation.
		result, err := tx.ExecContext(ctx, `DELETE FROM operation_locks WHERE target_type=? AND target_id=? AND operation_id=? AND NOT EXISTS (SELECT 1 FROM operations WHERE id=? AND state IN ('queued','running','waiting') AND target_type=? AND target_id=?)`, lock.TargetType, lock.TargetID, lock.OperationID, lock.OperationID, lock.TargetType, lock.TargetID)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 0 {
			continue
		}
		confirmed = append(confirmed, lock)
		event.Phase = "orphan_lock_detected"
		if err := recordTransition(ctx, tx, event); err != nil {
			return nil, err
		}
		event.Phase = "orphan_lock_repaired"
		if err := recordTransition(ctx, tx, event); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return confirmed, nil
}

// CreateExclusive reserves one persistent maintenance lock per target. The
// operation and lock are committed together; process restarts cannot release it.
func CreateExclusive(ctx context.Context, db *sql.DB, dialect string, op Operation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored, lookupErr := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, op.ID))
	if lookupErr == nil {
		if stored.Type != op.Type || stored.TargetType != op.TargetType || stored.TargetID != op.TargetID {
			return fmt.Errorf("operation ID %s already belongs to another request", op.ID)
		}
		if Terminal(stored.State) {
			return fmt.Errorf("operation %s is already terminal", op.ID)
		}
		op = stored
	}
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM operation_locks WHERE target_type=? AND target_id=?`, op.TargetType, op.TargetID).Scan(&existing)
	if err == nil {
		if existing == op.ID {
			return tx.Commit()
		}
		return Conflict{existing}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operation_locks(target_type,target_id,operation_id) VALUES(?,?,?)`, op.TargetType, op.TargetID, op.ID); err != nil {
		return err
	}
	if err = UpsertTx(ctx, tx, dialect, op); err != nil {
		return err
	}
	if err = recordTransition(ctx, tx, op); err != nil {
		return err
	}
	if AuditOrigin(ctx) == "cli" {
		event := op
		event.Phase = "cli_requested"
		if err := recordTransition(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Save releases the reservation only after a terminal result has been persisted.
func Save(ctx context.Context, db *sql.DB, dialect string, op Operation) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceExecutor(ctx, tx, op.ID); err != nil {
		return err
	}
	if err := fenceUnleasedNodeMutation(ctx, tx, dialect, op.ID); err != nil {
		return err
	}
	previous, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, op.ID))
	if err != nil {
		return err
	}
	if previous.Type != op.Type || previous.TargetType != op.TargetType || previous.TargetID != op.TargetID {
		return fmt.Errorf("operation identity cannot change")
	}
	if Terminal(previous.State) && (op.State != previous.State || op.Phase != previous.Phase) {
		return fmt.Errorf("terminal operation %s cannot transition", op.ID)
	}
	// A worker finishing after a process restart must retain the initiating actor
	// and request rather than replacing them with its background context.
	if previous.RequestID != "" {
		op.RequestID = previous.RequestID
	}
	if previous.RequestedBy != "" {
		op.RequestedBy = previous.RequestedBy
	}
	op.CreatedAt = previous.CreatedAt
	if err = UpsertTx(ctx, tx, dialect, op); err != nil {
		return err
	}
	if previous.State != op.State || previous.Phase != op.Phase {
		if err = recordTransition(ctx, tx, op); err != nil {
			return err
		}
	}
	if Terminal(op.State) {
		if _, err = tx.ExecContext(ctx, `DELETE FROM operation_locks WHERE operation_id=?`, op.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordTransition(ctx context.Context, tx *sql.Tx, op Operation) error {
	payload, err := json.Marshal(operationAuditPayload(ctx, op))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_events(operation_id,sequence,state,phase,observed_at,requested_by,request_id,event_type,payload_json)
 SELECT ?,COALESCE(MAX(sequence),0)+1,?,?,?,?,?,?,? FROM operation_events WHERE operation_id=?`, op.ID, op.State, op.Phase, op.UpdatedAt, op.RequestedBy, op.RequestID, operationAuditType(op), string(payload), op.ID)
	return err
}
