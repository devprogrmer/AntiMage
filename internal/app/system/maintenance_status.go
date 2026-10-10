package system

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"github.com/antimage/antimage/internal/platform/requestctx"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxMaintenanceLogLines = 80

var (
	percentagePattern = regexp.MustCompile(`(?i)(?:^|\s)(100|[1-9]?\d)(?:\.\d+)?%`)
	curlProgressRow   = regexp.MustCompile(`^(100|[1-9]?\d)\s+[\d.]+[kKmMgG]?\s+`)
)

type MaintenanceOperationSnapshot struct {
	TransactionRecoveryDispatched bool             `json:"transaction_recovery_dispatched,omitempty"`
	TransactionRecoveryAction     string           `json:"transaction_recovery_action,omitempty"`
	Origin                        string           `json:"origin,omitempty"`
	FinalizationDispatched        bool             `json:"finalization_dispatched,omitempty"`
	BackupIdentity                string           `json:"backup_identity,omitempty"`
	SourceVersion                 string           `json:"source_version,omitempty"`
	TargetPreviousVersion         string           `json:"target_previous_version,omitempty"`
	Reason                        string           `json:"reason,omitempty"`
	ResolvedTarget                *ResolvedInstall `json:"resolved_target,omitempty"`
	ID                            string           `json:"id"`
	TargetType                    string           `json:"target_type"`
	RequestedBy                   string           `json:"requested_by,omitempty"`
	RequestID                     string           `json:"request_id,omitempty"`
	Action                        string           `json:"action"`
	RequestedChannel              string           `json:"requested_channel,omitempty"`
	UpdatePolicy                  string           `json:"update_policy,omitempty"`
	RequestedVersion              string           `json:"requested_version,omitempty"`
	ResolvedVersion               string           `json:"resolved_version,omitempty"`
	DesiredVersion                string           `json:"desired_version,omitempty"`
	InstalledVersion              string           `json:"installed_version,omitempty"`
	RunningVersion                string           `json:"running_version,omitempty"`
	PreviousVersion               string           `json:"previous_version,omitempty"`
	StartedAtNanos                int64            `json:"started_at_unix_nano"`
	PhaseStartedAtNanos           int64            `json:"phase_started_at_unix_nano"`
	PreviousProcessStartedAt      int64            `json:"previous_process_started_at_unix_nano,omitempty"`
	Phase                         string           `json:"phase"`
	Message                       string           `json:"message"`
	Progress                      *int             `json:"progress"`
	Running                       bool             `json:"running"`
	Restarting                    bool             `json:"restarting"`
	Error                         string           `json:"error,omitempty"`
	RollbackError                 string           `json:"rollback_error,omitempty"`
	Logs                          []string         `json:"logs"`
	StartedAt                     int64            `json:"started_at"`
	UpdatedAt                     int64            `json:"updated_at"`
	FinishedAt                    *int64           `json:"finished_at,omitempty"`
	NeedsReload                   bool             `json:"needs_reload"`
}

type MaintenanceOperationStore struct {
	mu             sync.Mutex
	latest         MaintenanceOperationSnapshot
	subscribers    map[chan MaintenanceOperationSnapshot]struct{}
	db             *sql.DB
	dialect        string
	persistErr     error
	executionLease *operationapp.ExecutorLease
}

func NewMaintenanceOperationStore() *MaintenanceOperationStore {
	return &MaintenanceOperationStore{}
}

func NewMaintenanceOperationStoreWithDB(db *sql.DB, dialect string) *MaintenanceOperationStore {
	store := &MaintenanceOperationStore{db: db, dialect: strings.ToLower(strings.TrimSpace(dialect))}
	if db == nil {
		return store
	}
	history, err := store.History(1)
	if err != nil {
		store.persistErr = err
		return store
	}
	if len(history) > 0 {
		store.latest = history[0]
	}
	return store
}

func (s *MaintenanceOperationStore) PersistenceError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistErr
}

func (s *MaintenanceOperationStore) History(limit int) ([]MaintenanceOperationSnapshot, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.latest.ID == "" {
			return []MaintenanceOperationSnapshot{}, nil
		}
		return []MaintenanceOperationSnapshot{cloneMaintenanceOperation(s.latest)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := operationapp.ListTarget(ctx, s.db, "panel", "panel", limit)
	if err != nil {
		return nil, err
	}
	history := make([]MaintenanceOperationSnapshot, 0, len(items))
	for _, item := range items {
		payload, err := json.Marshal(item.Metadata["snapshot"])
		if err != nil {
			return nil, err
		}
		var snapshot MaintenanceOperationSnapshot
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.ID == "" {
			return nil, fmt.Errorf("panel operation %s has no snapshot", item.ID)
		}
		history = append(history, cloneMaintenanceOperation(snapshot))
	}
	return history, nil
}

func (s *MaintenanceOperationStore) persistLocked() {
	if s.db == nil || s.latest.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metadata := map[string]any{"snapshot": s.latest}
	if s.latest.Origin != "" {
		metadata["origin"] = s.latest.Origin
	}
	if s.executionLease != nil {
		ctx = operationapp.WithExecutorLease(ctx, *s.executionLease)
		metadata["executor_id"] = s.executionLease.ExecutorID
		metadata["lease_generation"] = s.executionLease.Generation
		metadata["resource_generation"] = s.executionLease.ResourceGeneration
	}
	generic := operationapp.Operation{
		ID: s.latest.ID, Type: s.latest.Action, TargetType: s.latest.TargetType,
		TargetID: "panel", RequestedBy: s.latest.RequestedBy, RequestID: s.latest.RequestID, State: operationapp.StateForPhase(s.latest.Phase), Phase: s.latest.Phase,
		Progress: s.latest.Progress, CreatedAt: s.latest.StartedAt, UpdatedAt: s.latest.UpdatedAt,
		CompletedAt: s.latest.FinishedAt, Error: firstString([]string{s.latest.Error, s.latest.RollbackError}), Metadata: metadata,
	}
	if s.latest.Phase != "queued" {
		started := s.latest.StartedAt
		generic.StartedAt = &started
	}
	_, err := operationapp.Get(ctx, s.db, generic.ID)
	if err == sql.ErrNoRows {
		s.persistErr = operationapp.CreateExclusive(ctx, s.db, s.dialect, generic)
	} else if err != nil {
		s.persistErr = err
	} else {
		s.persistErr = operationapp.Save(ctx, s.db, s.dialect, generic)
		if s.persistErr != nil {
			// Rejected stale writes must not publish a local false success.
			if stored, err := operationapp.Get(ctx, s.db, generic.ID); err == nil {
				if payload, err := json.Marshal(stored.Metadata["snapshot"]); err == nil {
					var authoritative MaintenanceOperationSnapshot
					if json.Unmarshal(payload, &authoritative) == nil && authoritative.ID == generic.ID {
						s.latest = authoritative
					}
				}
			}
		}
	}
}

func (s *MaintenanceOperationStore) SetCorrelation(id, requestID, requestedBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.RequestID = strings.TrimSpace(requestID)
	s.latest.RequestedBy = strings.TrimSpace(requestedBy)
	s.latest.UpdatedAt = time.Now().Unix()
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) Start(action string, args []string, message string, previousVersion ...string) MaintenanceOperationSnapshot {
	return s.StartWithContext(context.Background(), action, args, message, previousVersion...)
}

func (s *MaintenanceOperationStore) StartWithContext(ctx context.Context, action string, args []string, message string, previousVersion ...string) MaintenanceOperationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowTime := time.Now()
	now := nowTime.Unix()
	requestedVersion := requestedMaintenanceVersion(action, args)
	channel, policy := maintenanceUpdateIntent(action, args)
	op := MaintenanceOperationSnapshot{
		Origin:              operationapp.AuditOrigin(ctx),
		ID:                  fmt.Sprintf("%s-%d", action, time.Now().UnixNano()),
		TargetType:          "panel",
		RequestedBy:         requestctx.Admin(ctx),
		RequestID:           requestctx.ID(ctx),
		Action:              action,
		RequestedChannel:    channel,
		UpdatePolicy:        policy,
		RequestedVersion:    requestedVersion,
		DesiredVersion:      requestedVersion,
		PreviousVersion:     firstString(previousVersion),
		Phase:               "queued",
		Message:             message,
		Running:             true,
		Restarting:          false,
		Logs:                []string{"antimage " + strings.Join(args, " ")},
		StartedAt:           now,
		StartedAtNanos:      nowTime.UnixNano(),
		PhaseStartedAtNanos: nowTime.UnixNano(),
		UpdatedAt:           now,
	}
	progress := 0
	for i, arg := range args {
		if action == "rollback" && i+1 < len(args) {
			switch arg {
			case "--backup-id":
				op.BackupIdentity = args[i+1]
			case "--target-version":
				op.TargetPreviousVersion = args[i+1]
				op.DesiredVersion = args[i+1]
			case "--reason":
				op.Reason = args[i+1]
			}
			op.SourceVersion = op.PreviousVersion
		}
		if arg == "--resolved-build" && i+1 < len(args) {
			var target ResolvedInstall
			if err := json.Unmarshal([]byte(args[i+1]), &target); err != nil {
				s.persistErr = err
				return op
			}
			op.ResolvedTarget = &target
			op.RequestedChannel = target.RequestedChannel
			op.UpdatePolicy = target.RequestedPolicy
			op.RequestedVersion = target.RequestedVersion
			op.ResolvedVersion = target.Version
			op.DesiredVersion = target.Version
		}
	}
	op.Progress = &progress
	previous := s.latest
	s.latest = op
	s.persistLocked()
	if s.persistErr != nil {
		s.latest = previous
		return cloneMaintenanceOperation(op)
	}
	s.publishLocked()
	return cloneMaintenanceOperation(op)
}

func (s *MaintenanceOperationStore) SetPreviousProcessStartedAt(id string, startedAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.PreviousProcessStartedAt = startedAt
	s.latest.UpdatedAt = time.Now().Unix()
	s.persistLocked()
	s.publishLocked()
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func (s *MaintenanceOperationStore) MarkVerified(id, runningVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	now := time.Now().Unix()
	s.latest.Phase = "completed"
	s.latest.RunningVersion = strings.TrimSpace(runningVersion)
	s.latest.InstalledVersion = strings.TrimSpace(runningVersion)
	if s.latest.DesiredVersion == "latest" || s.latest.DesiredVersion == "dev" {
		s.latest.DesiredVersion = s.latest.ResolvedVersion
	}
	s.latest.Message = "Panel running version verified after restart: " + strings.TrimSpace(runningVersion)
	s.latest.Running = false
	s.latest.Restarting = false
	s.latest.NeedsReload = false
	s.latest.Error = ""
	s.latest.RollbackError = ""
	s.latest.UpdatedAt = now
	s.latest.FinishedAt = &now
	s.setProgressAtLeastLocked(100)
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkRollingBack(id, updateError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.Phase = "rolling_back"
	s.latest.PhaseStartedAtNanos = time.Now().UnixNano()
	s.latest.Message = "Requested panel version failed verification; restoring previous version"
	s.latest.Error = updateError
	s.latest.Running = true
	s.latest.Restarting = true
	s.latest.NeedsReload = true
	s.latest.UpdatedAt = time.Now().Unix()
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkFinalizing(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.FinalizationDispatched = true
	s.latest.Phase = "finalizing"
	s.latest.Message = "Runtime verified; waiting for operation-owned update finalization"
	s.latest.Running = true
	s.latest.UpdatedAt = time.Now().Unix()
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkTransactionRecovery(id, action string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.TransactionRecoveryDispatched = true
	s.latest.TransactionRecoveryAction = action
	s.latest.Phase = "reconciling"
	s.latest.Message = "Resuming the verified missing transaction step"
	s.latest.Running = true
	s.latest.UpdatedAt = time.Now().Unix()
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkRolledBack(id, runningVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	now := time.Now().Unix()
	s.latest.Phase = "rolled_back"
	s.latest.RunningVersion = strings.TrimSpace(runningVersion)
	s.latest.InstalledVersion = strings.TrimSpace(runningVersion)
	s.latest.Message = "Previous panel version restored and verified: " + strings.TrimSpace(runningVersion)
	s.latest.Running = false
	s.latest.Restarting = false
	s.latest.NeedsReload = false
	s.latest.RollbackError = ""
	s.latest.UpdatedAt = now
	s.latest.FinishedAt = &now
	s.setProgressAtLeastLocked(100)
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkRollbackFailed(id, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	now := time.Now().Unix()
	s.latest.Phase = "failed"
	s.latest.Message = "Panel update and rollback failed"
	if s.latest.Error == "" {
		s.latest.Error = "Requested version verification failed"
	}
	s.latest.RollbackError = detail
	s.latest.Running = false
	s.latest.Restarting = false
	s.latest.NeedsReload = false
	s.latest.UpdatedAt = now
	s.latest.FinishedAt = &now
	s.persistLocked()
	s.publishLocked()
}

func requestedMaintenanceVersion(action string, args []string) string {
	if action != "update" {
		return ""
	}
	for i, arg := range args {
		if arg == "--version" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if arg == "--dev" {
			return "dev"
		}
	}
	return "latest"
}

func maintenanceUpdateIntent(action string, args []string) (string, string) {
	if action != "update" {
		return "", ""
	}
	for i, arg := range args {
		if arg == "--dev" {
			return "dev", "latest"
		}
		if arg == "--version" && i+1 < len(args) {
			version := strings.TrimSpace(args[i+1])
			if strings.HasPrefix(version, "dev-") {
				return "dev", "pinned"
			}
			if version == "latest" {
				return "stable", "latest"
			}
			return "stable", "pinned"
		}
	}
	return "stable", "latest"
}

func (s *MaintenanceOperationStore) Latest() MaintenanceOperationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMaintenanceOperation(s.latest)
}

func (s *MaintenanceOperationStore) Get(id string) MaintenanceOperationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return MaintenanceOperationSnapshot{}
	}
	return cloneMaintenanceOperation(s.latest)
}

func (s *MaintenanceOperationStore) Subscribe() (<-chan MaintenanceOperationSnapshot, func()) {
	updates := make(chan MaintenanceOperationSnapshot, 1)
	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[chan MaintenanceOperationSnapshot]struct{})
	}
	s.subscribers[updates] = struct{}{}
	if s.latest.ID != "" {
		updates <- cloneMaintenanceOperation(s.latest)
	}
	s.mu.Unlock()
	return updates, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[updates]; ok {
			delete(s.subscribers, updates)
			close(updates)
		}
		s.mu.Unlock()
	}
}

func (s *MaintenanceOperationStore) AppendOutput(id string, line string) {
	cleaned := cleanMaintenanceLine(line)
	if cleaned == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.UpdatedAt = time.Now().Unix()
	s.latest.Logs = append(s.latest.Logs, cleaned)
	if len(s.latest.Logs) > maxMaintenanceLogLines {
		s.latest.Logs = append([]string{}, s.latest.Logs[len(s.latest.Logs)-maxMaintenanceLogLines:]...)
	}
	phase, message := classifyMaintenanceLine(cleaned, s.latest.Action)
	const resolvedVersionPrefix = "Resolved AntiMage version:"
	if s.latest.ResolvedTarget == nil && strings.HasPrefix(cleaned, resolvedVersionPrefix) {
		s.latest.ResolvedVersion = strings.TrimSpace(strings.TrimPrefix(cleaned, resolvedVersionPrefix))
		s.latest.DesiredVersion = s.latest.ResolvedVersion
	}
	if phase != "" {
		s.latest.Phase = phase
		s.setProgressAtLeastLocked(maintenancePhaseProgress(phase))
	}
	if message != "" {
		s.latest.Message = message
	}
	if progress, ok := extractMaintenanceProgress(cleaned); ok {
		s.setProgressAtLeastLocked(downloadProgress(progress))
		if s.latest.Phase == "queued" {
			s.latest.Phase = "downloading"
		}
	}
	if strings.Contains(strings.ToLower(cleaned), "restart") {
		s.latest.Restarting = true
		s.latest.NeedsReload = true
		s.setProgressAtLeastLocked(100)
	}
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkRestarting(id string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	s.latest.UpdatedAt = time.Now().Unix()
	s.latest.Phase = "restarting"
	s.latest.PhaseStartedAtNanos = time.Now().UnixNano()
	s.latest.Message = message
	s.latest.Restarting = true
	s.latest.NeedsReload = true
	s.latest.Running = true
	s.setProgressAtLeastLocked(100)
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) MarkOutcomeUnknown(id string) {
	s.markUnverifiedOutcome(id, "outcome_unknown", "Command interrupted; verifying actual runtime before any retry")
}

func (s *MaintenanceOperationStore) MarkManualRecoveryRequired(id string) {
	s.markUnverifiedOutcome(id, "manual_recovery_required", "Command outcome requires manual inspection; no destructive command replayed")
}

func (s *MaintenanceOperationStore) markUnverifiedOutcome(id, phase, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id || !s.latest.Running {
		return
	}
	s.latest.Phase, s.latest.Message = phase, message
	s.latest.UpdatedAt = time.Now().Unix()
	s.latest.Running = true
	s.latest.FinishedAt = nil
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) Finish(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest.ID != id {
		return
	}
	now := time.Now().Unix()
	s.latest.UpdatedAt = now
	s.latest.Running = false
	if err != nil {
		s.latest.FinishedAt = &now
		s.latest.Phase = "failed"
		s.latest.Message = "Command failed"
		s.latest.Error = err.Error()
		s.persistLocked()
		s.publishLocked()
		return
	}
	if s.latest.Action == "update" || s.latest.Action == "rollback" || s.latest.Action == "restart" || s.latest.Action == "soft-reload" {
		s.latest.Phase = "restarting"
		s.latest.Message = "AntiMage is restarting. Waiting for the API to come back."
		s.latest.Restarting = true
		s.latest.NeedsReload = true
		s.latest.Running = true
		s.setProgressAtLeastLocked(100)
		s.persistLocked()
		s.publishLocked()
		return
	}
	s.latest.FinishedAt = &now
	s.latest.Phase = "completed"
	s.latest.Message = "Operation completed"
	s.setProgressAtLeastLocked(100)
	s.persistLocked()
	s.publishLocked()
}

func (s *MaintenanceOperationStore) setProgressAtLeastLocked(progress int) {
	progress = clampPercent(progress)
	if s.latest.Progress != nil && *s.latest.Progress >= progress {
		return
	}
	s.latest.Progress = &progress
}

func (s *MaintenanceOperationStore) publishLocked() {
	if len(s.subscribers) == 0 {
		return
	}
	snapshot := cloneMaintenanceOperation(s.latest)
	for updates := range s.subscribers {
		select {
		case <-updates:
		default:
		}
		select {
		case updates <- snapshot:
		default:
		}
	}
}

func cloneMaintenanceOperation(op MaintenanceOperationSnapshot) MaintenanceOperationSnapshot {
	if op.ResolvedTarget != nil {
		target := *op.ResolvedTarget
		op.ResolvedTarget = &target
	}
	op.Logs = append([]string{}, op.Logs...)
	return op
}

func cleanMaintenanceLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	line = strings.ReplaceAll(line, "\r", " ")
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > 600 {
		line = line[:600]
	}
	return line
}

func classifyMaintenanceLine(line string, action string) (string, string) {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "download"):
		return "downloading", "Downloading the new AntiMage image"
	case strings.Contains(lower, "extract") || strings.Contains(lower, "unpack"):
		return "installing", "Extracting and installing the new binary"
	case strings.Contains(lower, "install"):
		return "installing", "Installing AntiMage files"
	case strings.Contains(lower, "migrat"):
		return "migrating", "Running database migrations"
	case strings.Contains(lower, "restart") || strings.Contains(lower, "stopping") || strings.Contains(lower, "started"):
		return "restarting", "AntiMage is restarting"
	case strings.Contains(lower, "selected") || strings.Contains(lower, "update"):
		if action == "update" {
			return "updating", "Updating AntiMage"
		}
	}
	return "", ""
}

func extractMaintenanceProgress(line string) (int, bool) {
	if match := percentagePattern.FindStringSubmatch(line); len(match) == 2 {
		value, err := strconv.Atoi(match[1])
		if err == nil {
			return clampPercent(value), true
		}
	}
	if match := curlProgressRow.FindStringSubmatch(line); len(match) == 2 {
		value, err := strconv.Atoi(match[1])
		if err == nil {
			return clampPercent(value), true
		}
	}
	return 0, false
}

func maintenancePhaseProgress(phase string) int {
	switch phase {
	case "updating":
		return 5
	case "downloading":
		return 10
	case "installing":
		return 88
	case "migrating":
		return 95
	case "restarting", "completed":
		return 100
	default:
		return 0
	}
}

func downloadProgress(value int) int {
	return 10 + clampPercent(value)*75/100
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
