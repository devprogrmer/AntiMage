package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

type Operation struct {
	ID          string         `json:"id"`
	Type        string         `json:"operation_type"`
	TargetType  string         `json:"target_type"`
	TargetID    string         `json:"target_id"`
	RequestedBy string         `json:"requested_by,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	State       string         `json:"state"`
	Phase       string         `json:"phase"`
	Progress    *int           `json:"progress,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	StartedAt   *int64         `json:"started_at,omitempty"`
	UpdatedAt   int64          `json:"updated_at"`
	CompletedAt *int64         `json:"completed_at,omitempty"`
	Error       string         `json:"error,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func StateForPhase(phase string) string {
	p := strings.ToLower(strings.TrimSpace(phase))
	switch p {
	case "queued", "pending":
		return "queued"
	case "waiting", "waiting_for_reconnect", "reconnecting":
		return "waiting"
	case "completed", "verified", "success":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "rolled_back":
		return "rolled_back"
	default:
		return "running"
	}
}

func Upsert(ctx context.Context, db *sql.DB, dialect string, op Operation) error {
	return upsert(ctx, db.ExecContext, dialect, op)
}

func UpsertTx(ctx context.Context, tx *sql.Tx, dialect string, op Operation) error {
	return upsert(ctx, tx.ExecContext, dialect, op)
}

type execContext func(context.Context, string, ...any) (sql.Result, error)

func upsert(ctx context.Context, exec execContext, dialect string, op Operation) error {
	metadata, err := json.Marshal(op.Metadata)
	if err != nil {
		return err
	}
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	var progress any
	if op.Progress != nil {
		progress = *op.Progress
	}
	var started, completed any
	if op.StartedAt != nil {
		started = *op.StartedAt
	}
	if op.CompletedAt != nil {
		completed = *op.CompletedAt
	}
	query := `INSERT INTO operations (id,operation_type,target_type,target_id,requested_by,request_id,state,phase,progress,created_at,started_at,updated_at,completed_at,error,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	args := []any{op.ID, op.Type, op.TargetType, op.TargetID, op.RequestedBy, op.RequestID, op.State, op.Phase, progress, op.CreatedAt, started, op.UpdatedAt, completed, op.Error, string(metadata)}
	if strings.EqualFold(dialect, "mysql") {
		query += ` ON DUPLICATE KEY UPDATE operation_type=VALUES(operation_type),target_type=VALUES(target_type),target_id=VALUES(target_id),requested_by=VALUES(requested_by),request_id=VALUES(request_id),state=VALUES(state),phase=VALUES(phase),progress=VALUES(progress),started_at=VALUES(started_at),updated_at=VALUES(updated_at),completed_at=VALUES(completed_at),error=VALUES(error),metadata_json=VALUES(metadata_json)`
	} else {
		query += ` ON CONFLICT(id) DO UPDATE SET operation_type=excluded.operation_type,target_type=excluded.target_type,target_id=excluded.target_id,requested_by=excluded.requested_by,request_id=excluded.request_id,state=excluded.state,phase=excluded.phase,progress=excluded.progress,started_at=excluded.started_at,updated_at=excluded.updated_at,completed_at=excluded.completed_at,error=excluded.error,metadata_json=excluded.metadata_json`
	}
	_, err = exec(ctx, query, args...)
	return err
}

func List(ctx context.Context, db *sql.DB, limit int) ([]Operation, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `SELECT id,operation_type,target_type,target_id,requested_by,request_id,state,phase,progress,created_at,started_at,updated_at,completed_at,error,metadata_json FROM operations ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Operation, 0)
	for rows.Next() {
		var op Operation
		var progress sql.NullInt64
		var started, completed sql.NullInt64
		var metadata string
		if err := rows.Scan(&op.ID, &op.Type, &op.TargetType, &op.TargetID, &op.RequestedBy, &op.RequestID, &op.State, &op.Phase, &progress, &op.CreatedAt, &started, &op.UpdatedAt, &completed, &op.Error, &metadata); err != nil {
			return nil, err
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
		_ = json.Unmarshal([]byte(metadata), &op.Metadata)
		result = append(result, op)
	}
	return result, rows.Err()
}

func Epoch(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Unix()
	return &value
}
