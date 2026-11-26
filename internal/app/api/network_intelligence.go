package api

import (
	"context"
	"net/http"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"github.com/antimage/antimage/internal/app/online"
)

const networkPresenceLimit = 250

type networkPresenceRow struct {
	UserID     int64     `json:"user_id"`
	Username   string    `json:"username"`
	NodeID     int64     `json:"node_id"`
	NodeName   string    `json:"node_name"`
	Protocol   string    `json:"vpn_protocol"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

func (s *Server) networkIntelligencePresenceRoute() http.HandlerFunc {
	handler := http.HandlerFunc(http.NotFound)
	if s.cfg.NetworkIntelligenceEnabled {
		handler = s.requireAdmin(s.handleNetworkIntelligencePresence)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		handler(w, r)
	}
}

func (s *Server) handleNetworkIntelligencePresence(w http.ResponseWriter, r *http.Request) {
	principal, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok || principal.Role != string(adminapp.RoleFullAccess) {
		writeError(w, http.StatusForbidden, "You're not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	cutoff := online.Cutoff(time.Now())
	records, err := s.nodeController.OnlineAccessRecords(ctx, nodecontroller.OnlineAccessQuery{
		Limit: networkPresenceLimit, Cutoff: cutoff,
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Network presence unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"observed_since": cutoff,
		"limit":          networkPresenceLimit,
		"observations":   networkPresenceRows(records),
	})
}

func networkPresenceRows(records []nodecontroller.UserOnlineIPRecord) []networkPresenceRow {
	type key struct {
		userID, nodeID int64
		protocol       string
	}
	// OnlineAccessRecords supplies newest observations first.
	rows := make([]networkPresenceRow, 0, min(len(records), networkPresenceLimit))
	index := make(map[key]int, cap(rows))
	for _, record := range records {
		id := key{record.UserID, record.NodeID, record.Protocol}
		if i, exists := index[id]; exists {
			if record.LastSeenAt.After(rows[i].LastSeenAt) {
				rows[i].LastSeenAt = record.LastSeenAt
			}
			continue
		}
		if len(rows) == networkPresenceLimit {
			break
		}
		index[id] = len(rows)
		rows = append(rows, networkPresenceRow{
			UserID: record.UserID, Username: record.Username,
			NodeID: record.NodeID, NodeName: record.NodeName,
			Protocol: record.Protocol, LastSeenAt: record.LastSeenAt,
		})
	}
	return rows
}
