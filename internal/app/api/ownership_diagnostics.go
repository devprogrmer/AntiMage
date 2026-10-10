package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/antimage/antimage/internal/app/diagnostics"
	"github.com/antimage/antimage/internal/app/migrations"
	operationapp "github.com/antimage/antimage/internal/app/operations"
)

func (s *Server) collectOwnershipDiagnostics(ctx context.Context, observations *[]diagnostics.Observation) (bool, error) {
	for _, table := range []string{"operation_locks", "operation_executor_leases"} {
		present, err := migrations.HasTable(ctx, s.db, s.dialect, table)
		if err != nil || !present {
			return false, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.target_type,l.target_id FROM operation_locks l LEFT JOIN operations o ON o.id=l.operation_id WHERE o.id IS NULL OR o.state IN ('completed','failed','cancelled','rolled_back') OR o.target_type<>l.target_type OR o.target_id<>l.target_id`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var targetType, targetID string
		if err := rows.Scan(&targetType, &targetID); err != nil {
			rows.Close()
			return false, err
		}
		*observations = append(*observations, diagnostics.Observation{Source: "operation-ownership", ResourceType: targetType, ResourceID: targetID, Severity: "warning", Code: "lock.orphan_detected", Summary: "Destructive resource lock has an invalid owner", Detail: "The owner is missing, terminal, or belongs to a different resource. Only definite orphan ownership may be repaired.", RecommendedAction: "Inspect ownership history and run the controller's safe orphan reconciliation."})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT o.target_type,o.target_id,l.executor_id,l.expires_at,l.generation FROM operations o LEFT JOIN operation_executor_leases l ON l.operation_id=o.id WHERE o.state IN ('running','waiting') AND o.updated_at<?`, time.Now().Add(-time.Minute).Unix())
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var targetType, targetID string
		var executor sql.NullString
		var expires, generation sql.NullInt64
		if err := rows.Scan(&targetType, &targetID, &executor, &expires, &generation); err != nil {
			rows.Close()
			return false, err
		}
		code, summary := "", ""
		if !executor.Valid || executor.String == "" {
			code, summary = "lease.executor_missing", "Active operation has no executor"
		} else if expires.Int64 <= time.Now().UnixMilli() && generation.Int64 >= 3 {
			code, summary = "lease.repeated_expiry", "Operation has repeatedly lost its executor lease"
		}
		if code != "" {
			*observations = append(*observations, diagnostics.Observation{Source: "operation-ownership", ResourceType: targetType, ResourceID: targetID, Severity: "warning", Code: code, Summary: summary, Detail: "Inspect the persisted operation and lease audit. Unknown remote outcomes retain ownership until reconciliation.", RecommendedAction: "Inspect runtime evidence before allowing a new destructive executor."})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	active, err := operationapp.ListActive(ctx, s.db)
	if err != nil {
		return false, err
	}
	for _, op := range active {
		var attempts int
		_, _ = fmt.Sscan(fmt.Sprint(op.Metadata["recovery_attempts"]), &attempts)
		if op.Phase == "manual_recovery_required" || attempts >= 3 {
			*observations = append(*observations, diagnostics.Observation{Source: "operation-ownership", ResourceType: op.TargetType, ResourceID: op.TargetID, Severity: "critical", Code: "recovery.manual_required", Summary: "Destructive operation requires manual recovery", Detail: "Recovery could not prove the outcome, or its durable attempt limit was reached. The resource remains reserved; raw command errors are omitted.", RecommendedAction: "Inspect production files, runtime identity and verified backup before releasing ownership."})
		}
	}
	return true, nil
}
