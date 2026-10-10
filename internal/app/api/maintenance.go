package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	"golang.org/x/net/websocket"
)

func (s *Server) handleMaintenanceInfo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/info" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	info, err := s.maintenanceService().Info(ctx)
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleMaintenanceVersions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/versions" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("target")))
	if target != "panel" && target != "node" {
		writeError(w, http.StatusBadRequest, "target must be panel or node")
		return
	}
	refresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true") || r.URL.Query().Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	catalog, err := s.maintenanceService().Versions(ctx, target, refresh)
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *Server) handleMaintenanceHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 25
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	operations, err := s.maintenanceService().History(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range operations {
		operations[i] = publicMaintenanceSnapshot(operations[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": operations})
}

func (s *Server) handleMaintenanceOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := operationapp.List(ctx, s.db, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read operation history")
		return
	}
	for i := range items {
		items[i] = publicOperation(items[i])
		events, err := operationapp.ListAudit(ctx, s.db, items[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unable to read operation audit history")
			return
		}
		if items[i].Metadata == nil {
			items[i].Metadata = make(map[string]any)
		}
		items[i].Metadata["audit_events"] = events
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": items})
}

func (s *Server) handleMaintenanceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/update" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var payload systemapp.MaintenanceUpdateRequest
	if err := decodeOptionalJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status, err := s.maintenanceService().Update(r.Context(), payload)
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "operation": publicMaintenanceSnapshot(status)})
}

func (s *Server) handleMaintenanceRestart(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/restart" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := s.maintenanceService().Restart(r.Context())
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "operation": publicMaintenanceSnapshot(status)})
}

func (s *Server) handleMaintenanceBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	backups, err := s.maintenanceService().Backups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read verified backups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

func (s *Server) handleMaintenanceRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var payload systemapp.RollbackRequest
	if err := decodeOptionalJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid rollback request")
		return
	}
	status, err := s.maintenanceService().Rollback(r.Context(), payload)
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "operation": publicMaintenanceSnapshot(status)})
}

func (s *Server) handleMaintenanceSoftReload(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/soft-reload" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	status, err := s.maintenanceService().SoftReload(r.Context())
	if err != nil {
		writeMaintenanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"message":   "Panel soft reload scheduled successfully",
		"operation": publicMaintenanceSnapshot(status),
	})
}

func (s *Server) handleMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/maintenance/status" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleMaintenanceStatusWebSocket(w, r)
		return
	}
	writeJSON(w, http.StatusOK, publicMaintenanceSnapshot(s.maintenanceService().Status()))
}

func (s *Server) handleMaintenanceStatusWebSocket(w http.ResponseWriter, r *http.Request) {
	operationID := strings.TrimSpace(r.URL.Query().Get("id"))
	websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		updates, unsubscribe := s.maintenanceService().Subscribe()
		defer unsubscribe()
		for {
			select {
			case <-r.Context().Done():
				return
			case status, ok := <-updates:
				if !ok {
					return
				}
				if operationID != "" && status.ID != operationID {
					continue
				}
				if err := websocket.JSON.Send(conn, publicMaintenanceSnapshot(status)); err != nil {
					return
				}
			}
		}
	}).ServeHTTP(w, r)
}

func (s *Server) maintenanceService() *systemapp.MaintenanceService {
	if s.maintenance == nil {
		s.maintenance = systemapp.NewMaintenanceServiceWithDB(s.db, s.dialect)
	}
	return s.maintenance
}

func writeMaintenanceError(w http.ResponseWriter, err error) {
	status, _ := systemapp.HTTPStatus(err)
	detail := "Maintenance request could not be accepted. Inspect diagnostics using the request ID."
	if status == http.StatusConflict {
		detail = "A conflicting maintenance operation owns this panel. Inspect active operation history."
	}
	writeError(w, status, detail)
}
