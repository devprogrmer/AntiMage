package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var auditIdentityValue = regexp.MustCompile(`^[\p{L}\p{N}_.+ -]{1,160}$`)

type AuditEvent struct {
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"event_type"`
	Timestamp int64          `json:"timestamp"`
	Evidence  map[string]any `json:"evidence"`
}

// AppendAudit records evidence without changing the operation's state. The
// operation row serializes concurrent writers and identical evidence is stored
// once, including across controller restarts.
func AppendAudit(ctx context.Context, db *sql.DB, id, phase string, evidence map[string]any) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET updated_at=updated_at WHERE id=?`, id); err != nil {
		return err
	}
	op, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM operations WHERE id=?`, id))
	if err != nil {
		return err
	}
	op.Phase, op.UpdatedAt, op.Metadata = phase, time.Now().UTC().Unix(), evidence
	payload := operationAuditPayload(ctx, op)
	// Timestamp belongs to the event row and is excluded from deduplication.
	delete(payload, "timestamp")
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Compare bytes in Go: MySQL's text collation may equate distinct command
	// identities whose case differs. Identity deduplication must be exact.
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM operation_events WHERE operation_id=? AND event_type=?`, id, operationAuditType(op))
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var previous string
		if err := rows.Scan(&previous); err != nil {
			rows.Close()
			return err
		}
		if previous == string(encoded) {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = tx.ExecContext(ctx, `INSERT INTO operation_events(operation_id,sequence,state,phase,observed_at,requested_by,request_id,event_type,payload_json) SELECT ?,COALESCE(MAX(sequence),0)+1,?,?,?,?,?,?,? FROM operation_events WHERE operation_id=?`, id, op.State, phase, op.UpdatedAt, op.RequestedBy, op.RequestID, operationAuditType(op), string(encoded), id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAudit returns bounded, sanitized evidence, including legacy rows whose
// arbitrary payload must not be exposed directly to browser clients.
func ListAudit(ctx context.Context, db *sql.DB, operationID string) ([]AuditEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT sequence,event_type,observed_at,COALESCE(payload_json,'{}') FROM operation_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 30`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var raw string
		if err := rows.Scan(&event.Sequence, &event.Type, &event.Timestamp, &raw); err != nil {
			return nil, err
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, err
		}
		event.Evidence = make(map[string]any)
		copyAuditIdentity(event.Evidence, fields, []string{"origin", "actor", "request_id", "operation_id", "command_id", "rollout_id", "target_type", "target_id", "executor_id", "lease_generation", "resource_generation", "node_capability_level", "requested_version", "resolved_version", "resolved_commit", "artifact_sha256", "previous_version", "running_version", "result", "error_code"})
		events = append(events, event)
	}
	return events, rows.Err()
}

func auditEvidenceFields(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	return fields
}

func copyAuditIdentity(payload, fields map[string]any, keys []string) {
	for _, key := range keys {
		if _, exists := payload[key]; exists {
			continue
		}
		switch value := fields[key].(type) {
		case string:
			if auditIdentityValue.MatchString(value) {
				payload[key] = value
			}
		case int, int64, float64, json.Number:
			payload[key] = value
		}
	}
}

type auditOriginKey struct{}

func WithAuditOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, auditOriginKey{}, origin)
}

func AuditOrigin(ctx context.Context) string {
	value, _ := ctx.Value(auditOriginKey{}).(string)
	switch value {
	case "cli", "api", "recovery", "system", "rollout":
		return value
	}
	return ""
}

// Only explicitly allowed identity/evidence fields enter the audit payload.
// Configs, credentials, URLs and arbitrary metadata are never copied here.
func operationAuditPayload(ctx context.Context, op Operation) map[string]any {
	payload := map[string]any{
		"event_type": operationAuditType(op), "origin": "system", "actor": op.RequestedBy,
		"request_id": op.RequestID, "operation_id": op.ID, "target_type": op.TargetType,
		"target_id": op.TargetID, "result": op.State, "timestamp": op.UpdatedAt,
	}
	keys := []string{"command_id", "rollout_id", "node_capability_level", "requested_channel", "requested_policy", "requested_version", "resolved_version", "resolved_commit", "artifact_name", "artifact_sha256", "artifact_size", "previous_version", "running_version", "error_code"}
	copyAuditIdentity(payload, op.Metadata, keys)
	for _, nested := range []string{"update", "snapshot"} {
		fields := auditEvidenceFields(op.Metadata[nested])
		copyAuditIdentity(payload, fields, keys)
		target, _ := fields["resolved_target"].(map[string]any)
		for destination, source := range map[string]string{"resolved_commit": "commit", "artifact_sha256": "sha256", "artifact_size": "size", "artifact_name": "artifact_name"} {
			copyAuditIdentity(payload, map[string]any{destination: target[source]}, []string{destination})
		}
	}
	if origin, ok := op.Metadata["origin"].(string); ok {
		if normalized := AuditOrigin(WithAuditOrigin(ctx, origin)); normalized != "" {
			payload["origin"] = normalized
		}
	}
	if _, explicit := op.Metadata["origin"]; !explicit && op.RequestedBy != "" {
		payload["origin"] = "api"
	}
	if origin := AuditOrigin(ctx); origin != "" {
		payload["origin"] = origin
	}
	if lease, ok := ExecutorLeaseFromContext(ctx); ok {
		payload["executor_id"] = lease.ExecutorID
		payload["lease_generation"] = lease.Generation
		payload["resource_generation"] = lease.ResourceGeneration
	}
	return payload
}

func operationAuditType(op Operation) string {
	switch op.Phase {
	case "fencing_rejected":
		return "fencing.rejected"
	case "watchdog_expired":
		return "watchdog.expired"
	case "cli_requested":
		return "cli.requested"
	case "recovery_started":
		return "recovery.started"
	case "lease_acquired":
		return "lease.acquired"
	case "lease_taken_over":
		return "lease.takeover"
	case "outcome_unknown":
		return "command.outcome_unknown"
	case "outcome_reconciled":
		return "command.outcome_reconciled"
	case "reconciling":
		return "command.reconciliation_started"
	case "manual_recovery_required":
		return "command.reconciliation_failed"
	case "orphan_lock_repaired":
		return "lock.orphan_repaired"
	case "orphan_lock_detected":
		return "lock.orphan_detected"
	}
	prefix := strings.TrimPrefix(op.Type, "node_")
	switch op.Type {
	case "core_update", "core_restart":
		prefix = "core"
	case "geo_update":
		prefix = "geo"
	case "sync_config":
		prefix = "syncconfig"
	case "host_reboot":
		prefix = "restart"
	}
	if prefix == "" {
		prefix = "operation"
	}
	return prefix + "." + op.Phase
}
