package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/antimage/antimage/internal/app/diagnostics"
	"github.com/antimage/antimage/internal/app/migrations"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	operationapp "github.com/antimage/antimage/internal/app/operations"
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
	if err := s.collectUpdateLifecycleDiagnostics(ctx, &observations); err != nil {
		return err
	}
	completeSources = append(completeSources, "node-operations", "version-drift", "update-lifecycle")
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
	ownershipComplete, err := s.collectOwnershipDiagnostics(ctx, &observations)
	if err != nil {
		return err
	}
	if ownershipComplete {
		completeSources = append(completeSources, "operation-ownership")
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

func (s *Server) collectUpdateLifecycleDiagnostics(ctx context.Context, observations *[]diagnostics.Observation) error {
	items, err := operationapp.List(ctx, s.db, 500)
	if err != nil {
		return err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt > items[j].CreatedAt
	})
	seen := map[string]bool{}
	for _, op := range items {
		if op.Type != "node_update" && op.Type != "node_rollback" && op.Type != "node_rollout" {
			continue
		}
		key := op.TargetType + ":" + op.TargetID
		if seen[key] {
			continue
		}
		seen[key] = true
		var metadata struct {
			Update nodecontroller.NodeUpdateOperation `json:"update"`
		}
		payload, err := json.Marshal(op.Metadata)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(payload, &metadata); err != nil {
			return err
		}
		severity, code, summary := "", "", ""
		switch {
		case metadata.Update.RecoveryError != "":
			severity, code, summary = "critical", "update.recovery_failed", "Interrupted operation could not be safely recovered"
		case metadata.Update.RollbackError != "":
			severity, code, summary = "critical", "update.rollback_failed", "Rollback failed; manual recovery required"
		case op.Phase == "canary_failed":
			severity, code, summary = "error", "rollout.canary_failed", "Rollout stopped after failed canary verification"
		case op.State == "failed":
			severity, code, summary = "error", "update.failed", "Update or rollback operation failed"
		case op.Phase == "waiting_for_reconnect" && ((!metadata.Update.ReconnectDeadline.IsZero() && !time.Now().Before(metadata.Update.ReconnectDeadline)) || (metadata.Update.ReconnectDeadline.IsZero() && time.Now().Unix()-op.UpdatedAt > 300)):
			severity, code, summary = "error", "update.reconnect_stuck", "Update is waiting beyond its reconnect deadline"
		}
		if code != "" {
			*observations = append(*observations, diagnostics.Observation{Source: "update-lifecycle", ResourceType: op.TargetType, ResourceID: op.TargetID, Severity: severity, Code: code, Summary: summary, Detail: "Inspect the persisted operation history for the correlated operation and request IDs. Raw command output is omitted.", RecommendedAction: "Inspect the current runtime and verified backup before retrying; do not repeat binary replacement without recovery verification."})
		}
	}
	return nil
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
				profile := nodecontroller.ClassifyDestructiveCapabilities(node.Capabilities)
				if node.Status == "connected" && (!profile.SupportsFencing || !profile.SupportsCommandIdempotency) {
					*observations = append(*observations, diagnostics.Observation{Source: "node-connectivity", ResourceType: "node", ResourceID: strconv.FormatInt(node.ID, 10), Severity: "warning", Code: "node.fencing_upgrade_required", Summary: "Node agent upgrade required for verified update and fencing", Detail: "This connected agent does not advertise shared destructive fencing and command idempotency. Destructive maintenance is constrained until support is available.", RecommendedAction: "Upgrade the node agent and its managed installer before requesting destructive maintenance."})
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
	rows, err := s.db.QueryContext(ctx, `SELECT target_id,metadata_json FROM operations o WHERE operation_type='node_update' AND target_type='node' AND id=(SELECT latest.id FROM operations latest WHERE latest.operation_type='node_update' AND latest.target_type='node' AND latest.target_id=o.target_id ORDER BY latest.created_at DESC,latest.id DESC LIMIT 1)`)
	if err != nil {
		return connectivityComplete, err
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, payload string
		if err := rows.Scan(&nodeID, &payload); err != nil {
			return connectivityComplete, err
		}
		var metadata struct {
			Update nodecontroller.NodeUpdateOperation `json:"update"`
		}
		if err := json.Unmarshal([]byte(payload), &metadata); err != nil {
			return connectivityComplete, err
		}
		u := metadata.Update
		if u.DesiredVersion == "" || u.RunningVersion == "" || u.DesiredVersion == u.RunningVersion {
			continue
		}
		*observations = append(*observations, diagnostics.Observation{Source: "version-drift", ResourceType: "node", ResourceID: nodeID, Severity: "warning", Code: "node.version_drift", Summary: "Node running version differs from desired target", Detail: "The persisted runtime evidence differs from the last update's desired version.", RecommendedAction: "Refresh node runtime evidence and inspect the update history."})
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
