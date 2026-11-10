package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/antimage/antimage/internal/app/nodecontroller"
)

type accountingInboxItem struct {
	NodeID     int64  `json:"node_id"`
	NodeName   string `json:"node_name"`
	Protocol   string `json:"protocol"`
	Kind       string `json:"kind"`
	OpenedAt   int64  `json:"opened_at"`
	ResolvedAt *int64 `json:"resolved_at,omitempty"`
	ReadAt     *int64 `json:"read_at,omitempty"`
}

// Only successfully sampled nodes may open or resolve an alert. A persistent
// alert causes a SELECT but no disk write on subsequent health sweeps.
func syncAccountingInbox(ctx context.Context, db *sql.DB, checked []int64, alerts []nodecontroller.AccountingAlert, now time.Time) error {
	if len(checked) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current := make(map[accountingAlertKey]nodecontroller.AccountingAlert, len(alerts))
	checkedSet := make(map[int64]struct{}, len(checked))
	for _, id := range checked {
		checkedSet[id] = struct{}{}
	}
	for _, alert := range alerts {
		if _, ok := checkedSet[alert.NodeID]; ok {
			current[accountingAlertKey{alert.NodeID, alert.Protocol, alert.Kind}] = alert
		}
	}
	stamp := now.Unix()
	for id := range checkedSet {
		rows, err := tx.QueryContext(ctx, `SELECT protocol, kind, resolved_at FROM node_accounting_alerts WHERE node_id = ?`, id)
		if err != nil {
			return err
		}
		known := make(map[accountingAlertKey]sql.NullInt64)
		for rows.Next() {
			var protocol, kind string
			var resolved sql.NullInt64
			if err := rows.Scan(&protocol, &kind, &resolved); err != nil {
				rows.Close()
				return err
			}
			known[accountingAlertKey{id, protocol, kind}] = resolved
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for key, resolved := range known {
			alert, active := current[key]
			if active && resolved.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE node_accounting_alerts SET node_name = ?, opened_at = ?, resolved_at = NULL, read_at = NULL WHERE node_id = ? AND protocol = ? AND kind = ?`, alert.NodeName, stamp, id, key.protocol, key.kind); err != nil {
					return err
				}
			} else if !active && !resolved.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE node_accounting_alerts SET resolved_at = ? WHERE node_id = ? AND protocol = ? AND kind = ?`, stamp, id, key.protocol, key.kind); err != nil {
					return err
				}
			}
		}
		for key, alert := range current {
			if key.nodeID != id {
				continue
			}
			if _, exists := known[key]; exists {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO node_accounting_alerts (node_id, protocol, kind, node_name, opened_at) VALUES (?, ?, ?, ?, ?)`, id, key.protocol, key.kind, alert.NodeName, stamp); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Server) handleAccountingInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT node_id, node_name, protocol, kind, opened_at, resolved_at, read_at FROM node_accounting_alerts ORDER BY opened_at DESC LIMIT 100`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "accounting alerts unavailable")
		return
	}
	defer rows.Close()
	items := make([]accountingInboxItem, 0)
	for rows.Next() {
		var item accountingInboxItem
		var resolved, read sql.NullInt64
		if err := rows.Scan(&item.NodeID, &item.NodeName, &item.Protocol, &item.Kind, &item.OpenedAt, &resolved, &read); err != nil {
			writeError(w, http.StatusInternalServerError, "accounting alerts unavailable")
			return
		}
		if resolved.Valid {
			item.ResolvedAt = &resolved.Int64
		}
		if read.Valid {
			item.ReadAt = &read.Int64
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "accounting alerts unavailable")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleAccountingInboxRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var key struct {
		NodeID   int64  `json:"node_id"`
		Protocol string `json:"protocol"`
		Kind     string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&key); err != nil || key.NodeID <= 0 || key.Protocol == "" || key.Kind == "" {
		writeError(w, http.StatusBadRequest, "invalid alert key")
		return
	}
	_, err := s.db.ExecContext(r.Context(), `UPDATE node_accounting_alerts SET read_at = ? WHERE node_id = ? AND protocol = ? AND kind = ? AND read_at IS NULL`, time.Now().Unix(), key.NodeID, key.Protocol, key.Kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "accounting alert could not be marked read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
