package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const ExecutorLeaseDDL = `CREATE TABLE operation_executor_leases (
 operation_id VARCHAR(96) NOT NULL PRIMARY KEY,
 executor_id VARCHAR(96) NOT NULL,
 acquired_at BIGINT NOT NULL,
 expires_at BIGINT NOT NULL,
 generation BIGINT NOT NULL,
 revision BIGINT NOT NULL
)`

var ErrLeaseHeld = errors.New("operation executor lease is held")
var ErrLeaseLost = errors.New("operation executor lease was lost")

const ResourceFenceDDL = `CREATE TABLE operation_resource_fences (
 target_type VARCHAR(48) NOT NULL,
 target_id VARCHAR(128) NOT NULL,
 generation BIGINT NOT NULL,
 owner_operation_id VARCHAR(96) NOT NULL,
 revision BIGINT NOT NULL,
 PRIMARY KEY(target_type,target_id)
)`

type ExecutorLease struct {
	TargetType         string `json:"target_type"`
	TargetID           string `json:"target_id"`
	ResourceGeneration int64  `json:"resource_generation"`
	Dialect            string `json:"-"`
	OperationID        string `json:"operation_id"`
	ExecutorID         string `json:"executor_id"`
	AcquiredAt         int64  `json:"lease_acquired_at"`
	ExpiresAt          int64  `json:"lease_expires_at"`
	Generation         int64  `json:"lease_generation"`
}

func executorClockSQL(dialect string) string {
	if strings.EqualFold(dialect, "mysql") {
		return `CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3))*1000 AS SIGNED)`
	}
	return `CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)`
}

type leaseContextKey struct{}
type nodeMutationKey struct{}

func WithNodeMutation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, nodeMutationKey{}, id)
}

func fenceUnleasedNodeMutation(ctx context.Context, tx *sql.Tx, dialect, id string) error {
	requested, ok := ctx.Value(nodeMutationKey{}).(string)
	if !ok {
		return nil
	}
	if requested != id {
		return ErrLeaseLost
	}
	if _, leased := ExecutorLeaseFromContext(ctx); leased {
		return nil
	}
	query := `INSERT INTO operation_executor_leases(operation_id,executor_id,acquired_at,expires_at,generation,revision) VALUES(?,'',0,0,0,0)`
	if strings.EqualFold(dialect, "mysql") {
		query += ` ON DUPLICATE KEY UPDATE operation_id=VALUES(operation_id)`
	} else {
		query += ` ON CONFLICT(operation_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, query, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE operation_executor_leases SET revision=revision+1 WHERE operation_id=? AND executor_id=''`, id)
	if err := requireLeaseWrite(result, err); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return ErrLeaseHeld
		}
		return err
	}
	return nil
}

func WithExecutorLease(ctx context.Context, lease ExecutorLease) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, lease)
}

func ExecutorLeaseFromContext(ctx context.Context) (ExecutorLease, bool) {
	lease, ok := ctx.Value(leaseContextKey{}).(ExecutorLease)
	return lease, ok
}

// Acquisition uses a database write predicate rather than SELECT-then-UPDATE.
// The retained row keeps the fencing generation monotonic after release/crash.
func AcquireExecutorLease(ctx context.Context, db *sql.DB, dialect, id, executor string, ttl time.Duration) (ExecutorLease, error) {
	var lease ExecutorLease
	if id == "" || executor == "" || len(executor) > 96 || ttl <= 0 {
		return lease, fmt.Errorf("valid operation, executor and lease duration are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return lease, err
	}
	defer tx.Rollback()
	query := `INSERT INTO operation_executor_leases(operation_id,executor_id,acquired_at,expires_at,generation,revision) VALUES(?,'',0,0,0,0)`
	if strings.EqualFold(dialect, "mysql") {
		query += ` ON DUPLICATE KEY UPDATE operation_id=VALUES(operation_id)`
	} else {
		query += ` ON CONFLICT(operation_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, query, id); err != nil {
		return lease, err
	}
	clock := executorClockSQL(dialect)
	result, err := tx.ExecContext(ctx, `UPDATE operation_executor_leases SET executor_id=?,acquired_at=`+clock+`,expires_at=`+clock+`+?,generation=generation+1,revision=revision+1 WHERE operation_id=? AND expires_at<=`+clock+` AND EXISTS(SELECT 1 FROM operations WHERE id=? AND state IN ('queued','running','waiting'))`, executor, ttl.Milliseconds(), id, id)
	if err != nil {
		return lease, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return lease, err
	}
	if count != 1 {
		return lease, ErrLeaseHeld
	}
	lease.OperationID = id
	lease.Dialect = dialect
	if err := tx.QueryRowContext(ctx, `SELECT executor_id,acquired_at,expires_at,generation FROM operation_executor_leases WHERE operation_id=?`, id).Scan(&lease.ExecutorID, &lease.AcquiredAt, &lease.ExpiresAt, &lease.Generation); err != nil {
		return lease, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT target_type,target_id FROM operations WHERE id=?`, id).Scan(&lease.TargetType, &lease.TargetID); err != nil {
		return lease, err
	}
	resourceSeed := `INSERT INTO operation_resource_fences(target_type,target_id,generation,owner_operation_id,revision) VALUES(?,?,0,'',0)`
	if strings.EqualFold(dialect, "mysql") {
		resourceSeed += ` ON DUPLICATE KEY UPDATE target_id=VALUES(target_id)`
	} else {
		resourceSeed += ` ON CONFLICT(target_type,target_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, resourceSeed, lease.TargetType, lease.TargetID); err != nil {
		return lease, err
	}
	resourceWrite, err := tx.ExecContext(ctx, `UPDATE operation_resource_fences SET generation=generation+1,owner_operation_id=?,revision=revision+1 WHERE target_type=? AND target_id=? AND EXISTS(SELECT 1 FROM operation_locks WHERE target_type=? AND target_id=? AND operation_id=?)`, id, lease.TargetType, lease.TargetID, lease.TargetType, lease.TargetID, id)
	if err := requireLeaseWrite(resourceWrite, err); err != nil {
		return lease, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM operation_resource_fences WHERE target_type=? AND target_id=?`, lease.TargetType, lease.TargetID).Scan(&lease.ResourceGeneration); err != nil {
		return lease, err
	}
	owner, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, id))
	if err != nil {
		return lease, err
	}
	owner.Phase = "lease_acquired"
	if lease.Generation > 1 {
		owner.Phase = "lease_taken_over"
	}
	owner.UpdatedAt = time.Now().UTC().Unix()
	if err := recordTransition(WithExecutorLease(ctx, lease), tx, owner); err != nil {
		return lease, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutorLease{}, err
	}
	return lease, nil
}

func RenewExecutorLease(ctx context.Context, db *sql.DB, lease ExecutorLease, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("positive lease duration is required")
	}
	clock := executorClockSQL(lease.Dialect)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceExecutor(WithExecutorLease(ctx, lease), tx, lease.OperationID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE operation_executor_leases SET expires_at=`+clock+`+?,revision=revision+1 WHERE operation_id=? AND executor_id=? AND generation=? AND expires_at>`+clock, ttl.Milliseconds(), lease.OperationID, lease.ExecutorID, lease.Generation)
	if err := requireLeaseWrite(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func ReleaseExecutorLease(ctx context.Context, db *sql.DB, lease ExecutorLease) error {
	result, err := db.ExecContext(ctx, `UPDATE operation_executor_leases SET executor_id='',expires_at=0,revision=revision+1 WHERE operation_id=? AND executor_id=? AND generation=?`, lease.OperationID, lease.ExecutorID, lease.Generation)
	return requireLeaseWrite(result, err)
}

func requireLeaseWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}

// The write holds the lease row until the transaction's operation update has
// committed. Takeover cannot interleave with an old generation's Save.
func fenceExecutor(ctx context.Context, tx *sql.Tx, id string) error {
	lease, ok := ExecutorLeaseFromContext(ctx)
	if !ok {
		return nil
	}
	if lease.OperationID != id {
		return ErrLeaseLost
	}
	result, err := tx.ExecContext(ctx, `UPDATE operation_executor_leases SET revision=revision+1 WHERE operation_id=? AND executor_id=? AND generation=? AND expires_at>`+executorClockSQL(lease.Dialect), id, lease.ExecutorID, lease.Generation)
	if err := requireLeaseWrite(result, err); err != nil {
		return err
	}
	resourceWrite, err := tx.ExecContext(ctx, `UPDATE operation_resource_fences SET revision=revision+1 WHERE target_type=? AND target_id=? AND generation=? AND owner_operation_id=?`, lease.TargetType, lease.TargetID, lease.ResourceGeneration, id)
	return requireLeaseWrite(resourceWrite, err)
}

// Check immediately before dispatch as well as fencing the final DB transition.
// Remote job deduplication/fencing is additionally required at the node boundary.
func CheckExecutorLease(ctx context.Context, db *sql.DB, id string) error {
	if _, ok := ExecutorLeaseFromContext(ctx); !ok {
		return ErrLeaseLost
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fenceExecutor(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Runtime projections need the same transactional fence as operation state.
func ExecFencedTarget(ctx context.Context, db *sql.DB, targetType, targetID, query string, args ...any) (sql.Result, error) {
	lease, ok := ExecutorLeaseFromContext(ctx)
	if !ok {
		return db.ExecContext(ctx, query, args...)
	}
	if lease.TargetType != targetType || lease.TargetID != targetID {
		return nil, ErrLeaseLost
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := fenceExecutor(ctx, tx, lease.OperationID); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// Schedulers may mutate untouched queued operations, but cannot override an
// executor's operation even after its lease expires. Expiry requires takeover.
func AuthorizeNodeMutation(ctx context.Context, db *sql.DB, id string) error {
	if lease, ok := ExecutorLeaseFromContext(ctx); ok {
		if lease.OperationID != id {
			return ErrLeaseLost
		}
		return nil // Save validates expiry/generation in its write transaction.
	}
	var owner string
	err := db.QueryRowContext(ctx, `SELECT executor_id FROM operation_executor_leases WHERE operation_id=?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner != "" {
		return ErrLeaseHeld
	}
	return nil
}
