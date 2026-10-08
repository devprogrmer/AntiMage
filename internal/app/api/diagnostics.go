package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/diagnostics"
	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	"github.com/antimage/antimage/internal/app/xrayconfig"
)

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	store := diagnostics.Store{DB: s.db}
	refresh := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "1") || strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("refresh")), "true")
	if refresh {
		if err := s.collectDiagnostics(ctx, r, store); err != nil {
			writeError(w, http.StatusBadGateway, "unable to refresh all diagnostic sources")
			return
		}
	}
	items, err := store.List(ctx, r.URL.Query().Get("status"), r.URL.Query().Get("severity"), r.URL.Query().Get("source"), 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read diagnostics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"diagnostics": items, "refreshed_at": time.Now().UTC().Unix()})
}

func (s *Server) handleDiagnosticAcknowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/maintenance/diagnostics/"))
	id = strings.TrimSuffix(id, "/acknowledge")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "diagnostic ID is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := (diagnostics.Store{DB: s.db}).SetStatus(ctx, id, "acknowledged"); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "diagnostic not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "unable to acknowledge diagnostic")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) collectDiagnostics(ctx context.Context, r *http.Request, store diagnostics.Store) error {
	if s.db == nil {
		return sql.ErrConnDone
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	observations := make([]diagnostics.Observation, 0, 8)
	completeSources := make([]string, 0, 6)
	migration, migrationErr := migrations.Status(ctx, s.db, s.dialect)
	if migrationErr != nil {
		observations = append(observations, diagnostics.Observation{Source: "migrations", ResourceType: "panel", Severity: "critical", Code: "migration.status_unavailable", Summary: "Migration state unavailable", Detail: "The migration runner could not read the database schema state.", RecommendedAction: "Inspect database connectivity and migration logs."})
	} else if migration.Dirty {
		observations = append(observations, diagnostics.Observation{Source: "migrations", ResourceType: "panel", Severity: "critical", Code: "migration.dirty", Summary: "Database migration is incomplete", Detail: migration.Message, RecommendedAction: "Review migration logs and restore the schema to a known state."})
	}
	if migrationErr == nil {
		completeSources = append(completeSources, "migrations")
	}
	stats, statsErr := s.systemStatsService().Stats(ctx, dashboardAdminContext(r))
	if statsErr != nil {
		observations = append(observations, diagnostics.Observation{Source: "runtime", ResourceType: "panel", Severity: "error", Code: "panel.stats_unavailable", Summary: "Panel runtime metrics unavailable", Detail: "The system metrics service returned an error.", RecommendedAction: "Inspect panel runtime and system metric collection."})
	} else {
		completeSources = append(completeSources, "runtime", "xray")
		if stats.Disk.Percent >= 90 {
			observations = append(observations, diagnostics.Observation{Source: "runtime", ResourceType: "panel", Severity: "critical", Code: "disk.pressure", Summary: "Disk usage is critical", Detail: "Reported disk use is at least 90 percent.", RecommendedAction: "Inspect disk usage and free capacity before updates or backups."})
		} else if stats.Disk.Percent >= 80 {
			observations = append(observations, diagnostics.Observation{Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "disk.pressure", Summary: "Disk usage is high", Detail: "Reported disk use is at least 80 percent.", RecommendedAction: "Review disk usage and clean up unneeded files."})
		}
		if stats.Memory.Percent >= 90 {
			observations = append(observations, diagnostics.Observation{Source: "runtime", ResourceType: "panel", Severity: "critical", Code: "memory.pressure", Summary: "Memory usage is critical", Detail: "Reported memory use is at least 90 percent.", RecommendedAction: "Inspect memory-consuming processes and capacity."})
		} else if stats.Memory.Percent >= 80 {
			observations = append(observations, diagnostics.Observation{Source: "runtime", ResourceType: "panel", Severity: "warning", Code: "memory.pressure", Summary: "Memory usage is high", Detail: "Reported memory use is at least 80 percent.", RecommendedAction: "Review memory use and available capacity."})
		}
		if stats.LastXrayError != nil && strings.TrimSpace(*stats.LastXrayError) != "" {
			observations = append(observations, diagnostics.Observation{Source: "xray", ResourceType: "panel", Severity: "error", Code: "xray.runtime_error", Summary: "Xray reported a runtime error", Detail: "The runtime has a recorded Xray error. Raw log text is intentionally not persisted in diagnostics.", RecommendedAction: "Inspect Xray logs and validate the active configuration."})
		}
	}
	storedConfig, configErr := s.configRepo.GetTargetRawConfig(ctx, xrayconfig.MasterTargetID)
	if configErr != nil {
		observations = append(observations, diagnostics.Observation{Source: "xray-config", ResourceType: "panel", Severity: "error", Code: "xray.config_unavailable", Summary: "Stored Xray configuration could not be read", Detail: "The saved configuration was unavailable to the validation adapter; raw values are omitted.", RecommendedAction: "Inspect Xray configuration storage and database health."})
	} else {
		completeSources = append(completeSources, "xray-config")
		if _, validationErr := xrayconfig.Parse(storedConfig, xrayconfig.Options{}); validationErr != nil {
			observations = append(observations, diagnostics.Observation{Source: "xray-config", ResourceType: "panel", Severity: "error", Code: "xray.config_invalid", Summary: "Stored Xray configuration failed AntiMage validation", Detail: "The saved configuration did not pass the AntiMage configuration validator. The validator error is omitted to avoid persisting values that may contain secrets.", RecommendedAction: "Open Xray settings, review the reported validation error there, and validate before applying."})
		}
	}
	if err := s.collectFailedOperationDiagnostics(ctx, &observations); err != nil {
		return err
	}
	completeSources = append(completeSources, "node-operations", "version-drift")
	backupSettings, backupErr := s.telegramRepo.Settings(ctx)
	if backupErr != nil {
		observations = append(observations, diagnostics.Observation{Source: "backup", ResourceType: "panel", Severity: "warning", Code: "backup.status_unavailable", Summary: "Backup delivery status could not be read", Detail: "The stored backup delivery state is unavailable; no raw error details are persisted.", RecommendedAction: "Inspect backup and Telegram delivery settings."})
	} else {
		completeSources = append(completeSources, "backup")
		if backupSettings.BackupLastError != nil && strings.TrimSpace(*backupSettings.BackupLastError) != "" {
			observations = append(observations, diagnostics.Observation{Source: "backup", ResourceType: "panel", Severity: "error", Code: "backup.last_delivery_failed", Summary: "The latest scheduled backup delivery failed", Detail: "A failure is recorded for the latest backup delivery. The raw provider error is omitted to protect credentials.", RecommendedAction: "Review backup delivery configuration and run a controlled backup check."})
		}
	}
	if s.certificateManager != nil {
		certificates, certErr := s.certificateManager.List(ctx)
		if certErr != nil {
			observations = append(observations, diagnostics.Observation{Source: "certificates", ResourceType: "panel", Severity: "warning", Code: "certificate.status_unavailable", Summary: "Certificate expiry could not be checked", Detail: "The certificate inventory was unavailable during this diagnostics refresh.", RecommendedAction: "Inspect certificate inventory and refresh diagnostics after recovery."})
		} else {
			completeSources = append(completeSources, "certificates")
			for _, certificate := range certificates {
				if certificate.NotAfter == nil || certificate.Status == "revoked" {
					continue
				}
				expires, parseErr := time.Parse(time.RFC3339, *certificate.NotAfter)
				if parseErr != nil {
					continue
				}
				if expires.After(now.Add(30 * 24 * time.Hour)) {
					continue
				}
				severity, code, summary := "warning", "certificate.expiring", "Certificate expires within 30 days"
				if !expires.After(now) {
					severity, code, summary = "critical", "certificate.expired", "Certificate has expired"
				}
				observations = append(observations, diagnostics.Observation{Source: "certificates", ResourceType: "certificate", ResourceID: certificate.Domain, Severity: severity, Code: code, Summary: summary, Detail: "The stored certificate expiry metadata is within the warning window.", RecommendedAction: "Renew or replace the certificate and verify the served certificate."})
			}
		}
	}
	connectivityComplete, err := s.collectNodeDiagnostics(ctx, &observations)
	if err != nil {
		return err
	}
	if connectivityComplete {
		completeSources = append(completeSources, "node-connectivity")
	}
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		item, err := store.Observe(ctx, observation, now)
		if err != nil {
			return err
		}
		seen[item.ID] = struct{}{}
	}
	// Resolve only after every source above returned successfully.
	return store.ResolveMissingSources(ctx, seen, now, completeSources)
}

func (s *Server) collectNodeDiagnostics(ctx context.Context, observations *[]diagnostics.Observation) (bool, error) {
	connectivityComplete := false
	if s.nodeController.Configured() {
		nodeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		result, err := s.nodeController.List(nodeCtx, nodecontroller.Request{IncludeMetrics: true})
		cancel()
		if err != nil {
			*observations = append(*observations, diagnostics.Observation{Source: "node-connectivity", ResourceType: "panel", Severity: "warning", Code: "node.collector_unavailable", Summary: "Node connectivity could not be refreshed", Detail: "The node metrics request did not complete; existing node findings were preserved.", RecommendedAction: "Check node-agent reachability and refresh diagnostics after recovery."})
		} else {
			connectivityComplete = true
			for _, node := range result.Nodes {
				if node.Status == "disabled" || node.Status == "limited" {
					continue
				}
				if node.Status == "error" || node.AgentStatus == "degraded" {
					detail := "The node agent did not return healthy runtime metrics."
					if node.Message != nil && strings.TrimSpace(*node.Message) != "" {
						detail = "The node agent reported a connectivity or runtime error; inspect the node details for the message."
					}
					*observations = append(*observations, diagnostics.Observation{Source: "node-connectivity", ResourceType: "node", ResourceID: strconv.FormatInt(node.ID, 10), Severity: "error", Code: "node.agent_unhealthy", Summary: "Node agent is not healthy", Detail: detail, RecommendedAction: "Inspect node connectivity and runtime health."})
				}
			}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.node_id, u.resolved_version, u.running_version FROM node_update_operations u WHERE u.id = (SELECT latest.id FROM node_update_operations latest WHERE latest.node_id = u.node_id ORDER BY latest.started_at DESC LIMIT 1) AND COALESCE(u.resolved_version,'') <> '' AND COALESCE(u.running_version,'') <> '' AND u.resolved_version <> u.running_version`)
	if err != nil {
		return connectivityComplete, err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID int64
		var desired, running string
		if err := rows.Scan(&nodeID, &desired, &running); err != nil {
			return connectivityComplete, err
		}
		*observations = append(*observations, diagnostics.Observation{Source: "version-drift", ResourceType: "node", ResourceID: strconv.FormatInt(nodeID, 10), Severity: "warning", Code: "node.version_drift", Summary: "Node running version differs from resolved target", Detail: "The node's reported running version differs from the last update's resolved version.", RecommendedAction: "Inspect the node update history and reported runtime version."})
	}
	return connectivityComplete, rows.Err()
}

func (s *Server) collectFailedOperationDiagnostics(ctx context.Context, observations *[]diagnostics.Observation) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, operation_type, status FROM node_operations WHERE status='failed' ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, nodeID int64
		var operation, status string
		if err := rows.Scan(&id, &nodeID, &operation, &status); err != nil {
			return err
		}
		*observations = append(*observations, diagnostics.Observation{Source: "node-operations", ResourceType: "node", ResourceID: strconv.FormatInt(nodeID, 10), Severity: "error", Code: "node.operation_failed." + strconv.FormatInt(id, 10), Summary: "Node operation failed: " + operation, Detail: "The persisted node operation has status " + status + ".", RecommendedAction: "Inspect the operation result and retry only after resolving its cause."})
	}
	return rows.Err()
}
