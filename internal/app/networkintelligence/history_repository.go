package networkintelligence

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"time"
)

// HistoryRepository serializes local writes. Deployments with multiple writers
// must provide a single authoritative sampling worker before enabling writes.
type HistoryRepository struct {
	DB *sql.DB
	mu sync.Mutex
}

type DurableQualityPoint struct {
	NodeID, UserID                         int64
	ObservedAt                             time.Time
	ACKRTTMilliseconds, JitterMilliseconds *float64
	RetransmissionDelta                    *int64
}

func validMilliseconds(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 600000)
}

// Prune removes expired records even when no new samples are written.
func (r *HistoryRepository) Prune(ctx context.Context, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.DB.ExecContext(ctx, "DELETE FROM network_quality_history WHERE observed_at < ?", now.Add(-7*24*time.Hour).UnixMilli())
	return err
}

// Add stores endpoint-free aggregates with seven-day retention and a hard
// 100000-row admission bound. Duplicate timestamps are idempotent.
func (r *HistoryRepository) Add(ctx context.Context, p DurableQualityPoint, now time.Time) error {
	if r.DB == nil || p.NodeID <= 0 || p.UserID <= 0 || p.ObservedAt.IsZero() || p.ObservedAt.After(now) || p.ObservedAt.Before(now.Add(-7*24*time.Hour)) || !validMilliseconds(p.ACKRTTMilliseconds) || !validMilliseconds(p.JitterMilliseconds) || (p.RetransmissionDelta != nil && *p.RetransmissionDelta < 0) {
		return errors.New("invalid durable quality point")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM network_quality_history WHERE observed_at < ?", now.Add(-7*24*time.Hour).UnixMilli()); err != nil {
		return err
	}
	var existing int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM network_quality_history WHERE node_id = ? AND user_id = ? AND observed_at = ?", p.NodeID, p.UserID, p.ObservedAt.UnixMilli()).Scan(&existing)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM network_quality_history").Scan(&count); err != nil {
		return err
	}
	if count >= 100000 {
		return errors.New("quality history capacity reached")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO network_quality_history (node_id,user_id,observed_at,ack_rtt_ms,jitter_ms,retransmission_delta) VALUES (?,?,?,?,?,?)", p.NodeID, p.UserID, p.ObservedAt.UnixMilli(), p.ACKRTTMilliseconds, p.JitterMilliseconds, p.RetransmissionDelta)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *HistoryRepository) List(ctx context.Context, nodeID, userID int64, since time.Time) ([]DurableQualityPoint, error) {
	if r.DB == nil || nodeID <= 0 || userID <= 0 {
		return nil, errors.New("valid history scope required")
	}
	rows, err := r.DB.QueryContext(ctx, "SELECT observed_at,ack_rtt_ms,jitter_ms,retransmission_delta FROM network_quality_history WHERE node_id=? AND user_id=? AND observed_at>=? ORDER BY observed_at DESC LIMIT 500", nodeID, userID, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DurableQualityPoint{}
	for rows.Next() {
		p := DurableQualityPoint{NodeID: nodeID, UserID: userID}
		var at int64
		if err := rows.Scan(&at, &p.ACKRTTMilliseconds, &p.JitterMilliseconds, &p.RetransmissionDelta); err != nil {
			return nil, err
		}
		p.ObservedAt = time.UnixMilli(at).UTC()
		result = append(result, p)
	}
	return result, rows.Err()
}
