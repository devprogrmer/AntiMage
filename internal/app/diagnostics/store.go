package diagnostics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Record struct {
	ID                string `json:"id"`
	Source            string `json:"source"`
	ResourceType      string `json:"resource_type"`
	ResourceID        string `json:"resource_id"`
	Severity          string `json:"severity"`
	Code              string `json:"code"`
	Summary           string `json:"summary"`
	Detail            string `json:"detail"`
	FirstSeenAt       int64  `json:"first_seen_at"`
	LastSeenAt        int64  `json:"last_seen_at"`
	OccurrenceCount   int64  `json:"occurrence_count"`
	Status            string `json:"status"`
	RecommendedAction string `json:"recommended_action"`
}

type Observation struct{ Source, ResourceType, ResourceID, Severity, Code, Summary, Detail, RecommendedAction string }

type Store struct{ DB *sql.DB }

func diagnosticID(source, resourceType, resourceID, code string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{source, resourceType, resourceID, code}, "\x00")))
	return "diag-" + hex.EncodeToString(sum[:16])
}

func (s Store) Observe(ctx context.Context, value Observation, observedAt time.Time) (Record, error) {
	if s.DB == nil {
		return Record{}, fmt.Errorf("diagnostics database is nil")
	}
	value.Source, value.ResourceType, value.ResourceID, value.Severity, value.Code = strings.TrimSpace(value.Source), strings.TrimSpace(value.ResourceType), strings.TrimSpace(value.ResourceID), strings.TrimSpace(value.Severity), strings.TrimSpace(value.Code)
	if value.Source == "" || value.ResourceType == "" || value.Severity == "" || value.Code == "" {
		return Record{}, fmt.Errorf("diagnostic source, resource type, severity, and code are required")
	}
	now := observedAt.UTC().Unix()
	id := diagnosticID(value.Source, value.ResourceType, value.ResourceID, value.Code)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	var record Record
	err = tx.QueryRowContext(ctx, `SELECT id, source, resource_type, resource_id, severity, code, summary, detail, first_seen_at, last_seen_at, occurrence_count, status, recommended_action FROM diagnostics WHERE id = ?`, id).Scan(&record.ID, &record.Source, &record.ResourceType, &record.ResourceID, &record.Severity, &record.Code, &record.Summary, &record.Detail, &record.FirstSeenAt, &record.LastSeenAt, &record.OccurrenceCount, &record.Status, &record.RecommendedAction)
	if err == sql.ErrNoRows {
		record = Record{ID: id, Source: value.Source, ResourceType: value.ResourceType, ResourceID: value.ResourceID, Severity: value.Severity, Code: value.Code, Summary: value.Summary, Detail: value.Detail, FirstSeenAt: now, LastSeenAt: now, OccurrenceCount: 1, Status: "active", RecommendedAction: value.RecommendedAction}
		_, err = tx.ExecContext(ctx, `INSERT INTO diagnostics (id,source,resource_type,resource_id,severity,code,summary,detail,first_seen_at,last_seen_at,occurrence_count,status,recommended_action) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.Source, record.ResourceType, record.ResourceID, record.Severity, record.Code, record.Summary, record.Detail, record.FirstSeenAt, record.LastSeenAt, record.OccurrenceCount, record.Status, record.RecommendedAction)
	} else if err == nil {
		count := record.OccurrenceCount
		if now-record.LastSeenAt >= int64((5 * time.Minute).Seconds()) {
			count++
		}
		_, err = tx.ExecContext(ctx, `UPDATE diagnostics SET severity=?,summary=?,detail=?,last_seen_at=?,occurrence_count=?,status=CASE WHEN status='resolved' THEN 'active' ELSE status END,recommended_action=? WHERE id=?`, value.Severity, value.Summary, value.Detail, now, count, value.RecommendedAction, id)
		if record.Status == "resolved" {
			record.Status = "active"
		}
		record.Severity, record.Summary, record.Detail, record.LastSeenAt, record.OccurrenceCount, record.RecommendedAction = value.Severity, value.Summary, value.Detail, now, count, value.RecommendedAction
	}
	if err != nil {
		return Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s Store) ResolveMissing(ctx context.Context, seen map[string]struct{}, now time.Time) error {
	return s.ResolveMissingSources(ctx, seen, now, nil)
}

func (s Store) ResolveMissingSources(ctx context.Context, seen map[string]struct{}, now time.Time, sources []string) error {
	query := `SELECT id FROM diagnostics WHERE status IN ('active','acknowledged')`
	args := []any{}
	if len(sources) > 0 {
		placeholders := make([]string, len(sources))
		for index, source := range sources {
			placeholders[index] = "?"
			args = append(args, source)
		}
		query += ` AND source IN (` + strings.Join(placeholders, ",") + `)`
	} else if sources != nil {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var resolve []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if _, ok := seen[id]; !ok {
			resolve = append(resolve, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range resolve {
		if _, err := s.DB.ExecContext(ctx, `UPDATE diagnostics SET status='resolved', last_seen_at=? WHERE id=? AND status IN ('active','acknowledged')`, now.UTC().Unix(), id); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) List(ctx context.Context, status, severity, source string, limit int) ([]Record, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	query := `SELECT id,source,resource_type,resource_id,severity,code,summary,detail,first_seen_at,last_seen_at,occurrence_count,status,recommended_action FROM diagnostics WHERE (?='' OR status=?) AND (?='' OR severity=?) AND (?='' OR source=?) ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'error' THEN 1 WHEN 'warning' THEN 2 ELSE 3 END, last_seen_at DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, query, status, status, severity, severity, source, source, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Record, 0)
	for rows.Next() {
		var item Record
		if err := rows.Scan(&item.ID, &item.Source, &item.ResourceType, &item.ResourceID, &item.Severity, &item.Code, &item.Summary, &item.Detail, &item.FirstSeenAt, &item.LastSeenAt, &item.OccurrenceCount, &item.Status, &item.RecommendedAction); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s Store) SetStatus(ctx context.Context, id, status string) error {
	if status != "acknowledged" && status != "active" {
		return fmt.Errorf("status must be acknowledged or active")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE diagnostics SET status=? WHERE id=? AND status<>'resolved'`, status, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}
