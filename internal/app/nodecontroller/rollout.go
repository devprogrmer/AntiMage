package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	systemapp "github.com/antimage/antimage/internal/app/system"
	"github.com/antimage/antimage/internal/platform/requestctx"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type RolloutRequest struct {
	Confirm     bool    `json:"confirm"`
	NodeIDs     []int64 `json:"node_ids"`
	Channel     string  `json:"channel"`
	Policy      string  `json:"policy"`
	Version     string  `json:"version"`
	Mode        string  `json:"mode"`
	CanaryCount int     `json:"canary_count"`
	Concurrency int     `json:"concurrency"`
}
type Rollout struct {
	Confirmed       bool                      `json:"confirmed"`
	ID              string                    `json:"id"`
	ResolvedTarget  systemapp.ResolvedInstall `json:"resolved_target"`
	Mode            string                    `json:"mode"`
	CanaryCount     int                       `json:"canary_count"`
	Concurrency     int                       `json:"concurrency"`
	Children        []string                  `json:"children"`
	RetryOf         string                    `json:"retry_of,omitempty"`
	CancelRequested bool                      `json:"cancel_requested"`
	Result          string                    `json:"result,omitempty"`
}
type RolloutView struct {
	CannotCancel []string               `json:"cannot_cancel"`
	Operation    operationapp.Operation `json:"operation"`
	Rollout      Rollout                `json:"rollout"`
	Children     []NodeUpdateOperation  `json:"children"`
	Summary      map[string]int         `json:"summary"`
}

func validateRollout(req *RolloutRequest) error {
	if req.Concurrency == 0 {
		req.Concurrency = 2
	}
	if req.Concurrency < 1 || req.Concurrency > 5 {
		return fmt.Errorf("rollout concurrency must be between 1 and 5")
	}
	if req.Mode == "" {
		req.Mode = "bulk"
	}
	if req.Mode != "bulk" && req.Mode != "canary" {
		return fmt.Errorf("rollout mode must be bulk or canary")
	}
	sort.Slice(req.NodeIDs, func(i, j int) bool { return req.NodeIDs[i] < req.NodeIDs[j] })
	ids := make([]int64, 0, len(req.NodeIDs))
	for _, id := range req.NodeIDs {
		if id <= 0 {
			return fmt.Errorf("node IDs must be positive")
		}
		if len(ids) == 0 || ids[len(ids)-1] != id {
			ids = append(ids, id)
		}
	}
	req.NodeIDs = ids
	if len(ids) == 0 || len(ids) > 100 {
		return fmt.Errorf("rollout requires between 1 and 100 nodes")
	}
	if req.Mode == "canary" && (req.CanaryCount < 1 || req.CanaryCount > len(ids)) {
		return fmt.Errorf("canary count must be between 1 and selected node count")
	}
	if req.Mode == "bulk" {
		req.CanaryCount = 0
	}
	return nil
}

func (c Controller) CreateRollout(ctx context.Context, req RolloutRequest) (RolloutView, error) {
	if err := validateRollout(&req); err != nil {
		return RolloutView{}, err
	}
	var osName, arch string
	previous := make(map[int64]string)
	for _, id := range req.NodeIDs {
		client, _, err := c.dial(ctx, id)
		if err != nil {
			return RolloutView{}, err
		}
		health, err := client.Control().Health(ctx, &nodev1.HealthRequest{})
		if err != nil {
			return RolloutView{}, err
		}
		if health == nil {
			return RolloutView{}, fmt.Errorf("node %d has no health evidence", id)
		}
		state := health.GetRuntime()
		if err := requireNodeUpdateEvidence(state); err != nil {
			return RolloutView{}, fmt.Errorf("node %d: %w", id, err)
		}
		if !state.GetConnected() || !state.GetStarted() {
			return RolloutView{}, fmt.Errorf("node %d is not healthy", id)
		}
		if osName == "" {
			osName, arch = state.GetOperatingSystem(), state.GetArchitecture()
		}
		if state.GetOperatingSystem() != osName || state.GetArchitecture() != arch || osName != "linux" || arch == "" {
			return RolloutView{}, fmt.Errorf("shared rollout requires the same verified Linux architecture")
		}
		previous[id] = state.GetNodeVersion()
	}
	checker := systemapp.NewGitHubUpdateChecker()
	checker.OS = osName
	checker.Arch = arch
	catalog, err := checker.Versions(ctx, "devprogrmer/AntiMage", "node", true)
	if err != nil {
		return RolloutView{}, err
	}
	target, err := systemapp.ResolveInstall(catalog, req.Channel, req.Policy, req.Version, osName, arch)
	if err != nil {
		return RolloutView{}, err
	}
	return c.persistRollout(ctx, req, target, previous, "")
}

func (c Controller) persistRollout(ctx context.Context, req RolloutRequest, target systemapp.ResolvedInstall, previous map[int64]string, retryOf string) (RolloutView, error) {
	now := time.Now().UTC()
	rollout := Rollout{ID: fmt.Sprintf("rollout-%d", now.UnixNano()), ResolvedTarget: target, Mode: req.Mode, CanaryCount: req.CanaryCount, Concurrency: req.Concurrency, RetryOf: retryOf, Confirmed: req.Confirm}
	tx, err := c.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return RolloutView{}, err
	}
	defer tx.Rollback()
	for index, id := range req.NodeIDs {
		childID := fmt.Sprintf("%s-node-%d-%d", rollout.ID, id, index)
		var blocked string
		err := tx.QueryRowContext(ctx, "SELECT operation_id FROM operation_locks WHERE target_type='node' AND target_id=?", fmt.Sprint(id)).Scan(&blocked)
		if err == nil {
			return RolloutView{}, fmt.Errorf("%w: %w", ErrNodeOperationConflict, operationapp.Conflict{BlockingID: blocked})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return RolloutView{}, err
		}
		// The insert is the authoritative concurrent reservation. A failing insert
		// rolls back every child and the parent, so no partial rollout is created.
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_operations WHERE node_id=? AND status IN ('pending','running','retrying') AND operation_type IN ('restart_service','update_runtime','update_geo','reboot_host','restart_node','reboot_node','update_service')", id).Scan(&active); err != nil {
			return RolloutView{}, err
		}
		if active > 0 {
			return RolloutView{}, fmt.Errorf("%w: node %d has active maintenance", ErrNodeOperationConflict, id)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO operation_locks(target_type,target_id,operation_id) VALUES('node',?,?)", fmt.Sprint(id), childID); err != nil {
			return RolloutView{}, err
		}
		child := NodeUpdateOperation{ID: childID, NodeID: id, RolloutID: rollout.ID, ResolvedTarget: &target, RequestedChannel: target.RequestedChannel, UpdatePolicy: target.RequestedPolicy, RequestedVersion: target.RequestedVersion, ResolvedVersion: target.Version, DesiredVersion: target.Version, PreviousVersion: previous[id], Phase: "queued", StartedAt: now, UpdatedAt: now}
		op := operationapp.Operation{ID: childID, Type: "node_update", TargetType: "node", TargetID: fmt.Sprint(id), RequestedBy: requestctx.Admin(ctx), RequestID: requestctx.ID(ctx), State: "queued", Phase: "queued", CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Metadata: map[string]any{"update": child}}
		if err := operationapp.UpsertTx(ctx, tx, c.repo.dialect, op); err != nil {
			return RolloutView{}, err
		}
		rollout.Children = append(rollout.Children, childID)
	}
	parent := operationapp.Operation{ID: rollout.ID, Type: "node_rollout", TargetType: "rollout", TargetID: rollout.ID, RequestedBy: requestctx.Admin(ctx), RequestID: requestctx.ID(ctx), State: "queued", Phase: "rollout_created", CreatedAt: now.Unix(), UpdatedAt: now.Unix(), Metadata: map[string]any{"rollout": rollout}}
	if !rollout.Confirmed {
		parent.Phase = "awaiting_confirmation"
	}
	if err := operationapp.UpsertTx(ctx, tx, c.repo.dialect, parent); err != nil {
		return RolloutView{}, err
	}
	if err := tx.Commit(); err != nil {
		return RolloutView{}, err
	}
	if rollout.Confirmed {
		c.launchRollout(rollout.ID)
	}
	return c.Rollout(ctx, rollout.ID)
}

func (c Controller) Rollout(ctx context.Context, id string) (RolloutView, error) {
	op, err := operationapp.Get(ctx, c.repo.db, id)
	if err != nil {
		return RolloutView{}, err
	}
	if op.Type != "node_rollout" {
		return RolloutView{}, fmt.Errorf("operation is not a node rollout")
	}
	payload, err := json.Marshal(op.Metadata["rollout"])
	if err != nil {
		return RolloutView{}, err
	}
	view := RolloutView{Operation: op, Children: []NodeUpdateOperation{}, Summary: map[string]int{"total": 0, "queued": 0, "running": 0, "completed": 0, "failed": 0, "rolled_back": 0, "cancelled": 0}}
	if err := json.Unmarshal(payload, &view.Rollout); err != nil {
		return view, err
	}
	for _, childID := range view.Rollout.Children {
		child, err := c.repo.NodeUpdateOperation(ctx, childID)
		if err != nil {
			return view, err
		}
		view.Children = append(view.Children, child)
		view.Summary["total"]++
		state := operationapp.StateForPhase(child.Phase)
		if state == "running" || state == "waiting" {
			view.CannotCancel = append(view.CannotCancel, child.ID)
		}
		if state == "waiting" {
			state = "running"
		}
		view.Summary[state]++
	}
	return view, nil
}
func (c Controller) rolloutMutex(id string) *sync.Mutex {
	value, _ := c.nodeLocks.LoadOrStore("rollout:"+id, &sync.Mutex{})
	return value.(*sync.Mutex)
}
func (c Controller) saveRollout(ctx context.Context, view RolloutView, phase, state, result string) error {
	view.Rollout.Result = result
	view.Operation.State = state
	view.Operation.Phase = phase
	view.Operation.UpdatedAt = time.Now().Unix()
	if operationapp.Terminal(state) {
		now := time.Now().Unix()
		view.Operation.CompletedAt = &now
	}
	view.Operation.Metadata["rollout"] = view.Rollout
	return operationapp.Save(ctx, c.repo.db, c.repo.dialect, view.Operation)
}
func (c Controller) CancelRollout(ctx context.Context, id string) (RolloutView, error) {
	mu := c.rolloutMutex(id)
	mu.Lock()
	defer mu.Unlock()
	view, err := c.Rollout(ctx, id)
	if err != nil {
		return view, err
	}
	if operationapp.Terminal(view.Operation.State) {
		return view, fmt.Errorf("cannot_cancel: rollout is already terminal")
	}
	view.Rollout.CancelRequested = true
	for _, child := range view.Children {
		if child.Phase == "queued" {
			if err := c.repo.AdvanceNodeUpdate(ctx, child.ID, "cancelled", 0, "", "", "", "", "", true); err != nil {
				return view, err
			}
		}
	}
	phase, state, result := "rollout_cancellation_requested", "running", ""
	if !view.Rollout.Confirmed {
		phase, state, result = "rollout_cancelled", "cancelled", "cancelled"
	}
	if err := c.saveRollout(ctx, view, phase, state, result); err != nil {
		return view, err
	}
	return c.Rollout(ctx, id)
}

func (c Controller) StartRollout(ctx context.Context, id string) (RolloutView, error) {
	mu := c.rolloutMutex(id)
	mu.Lock()
	defer mu.Unlock()
	view, err := c.Rollout(ctx, id)
	if err != nil {
		return view, err
	}
	if operationapp.Terminal(view.Operation.State) || view.Rollout.CancelRequested {
		return view, fmt.Errorf("rollout cannot be started")
	}
	view.Rollout.Confirmed = true
	if err := c.saveRollout(ctx, view, "rollout_created", "queued", ""); err != nil {
		return view, err
	}
	c.launchRollout(id)
	return c.Rollout(ctx, id)
}
func (c Controller) launchRollout(id string) {
	if _, active := c.rolloutWorkers.LoadOrStore(id, true); active {
		return
	}
	go func() { defer c.rolloutWorkers.Delete(id); c.runRollout(id) }()
}

func (c Controller) runRollout(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	mu := c.rolloutMutex(id)
	for {
		mu.Lock()
		view, err := c.Rollout(ctx, id)
		if err != nil || operationapp.Terminal(view.Operation.State) {
			mu.Unlock()
			return
		}
		if view.Rollout.Mode == "canary" {
			failed := false
			pending := false
			for i := 0; i < view.Rollout.CanaryCount; i++ {
				state := operationapp.StateForPhase(view.Children[i].Phase)
				if state == "failed" || state == "rolled_back" || state == "cancelled" {
					failed = true
				}
				if state != "completed" {
					pending = true
				}
			}
			if failed {
				for _, child := range view.Children {
					if child.Phase == "queued" {
						_ = c.repo.AdvanceNodeUpdate(ctx, child.ID, "cancelled", 0, "", "", "", "canary verification failed; target was not installed", "", true)
					}
				}
				_ = c.saveRollout(ctx, view, "canary_failed", "failed", "failed")
				mu.Unlock()
				return
			}
			if pending {
				view.Children = view.Children[:view.Rollout.CanaryCount]
			}
		}
		batch := []NodeUpdateOperation{}
		if !view.Rollout.CancelRequested {
			for _, child := range view.Children {
				if child.Phase == "queued" {
					batch = append(batch, child)
					if len(batch) == view.Rollout.Concurrency {
						break
					}
				}
			}
		}
		if len(batch) == 0 {
			// Never infer completion from the scheduler's queue alone.
			all, err := c.Rollout(ctx, id)
			if err != nil {
				mu.Unlock()
				return
			}
			if all.Summary["running"] > 0 {
				mu.Unlock()
				return
			}
			state, result, phase := "completed", "completed", "rollout_completed"
			if all.Rollout.CancelRequested {
				state, result, phase = "cancelled", "cancelled", "rollout_cancelled"
			} else if all.Summary["failed"]+all.Summary["rolled_back"] > 0 {
				state, result = "failed", "failed"
				phase = "rollout_failed"
				if all.Summary["completed"] > 0 {
					result = "completed_with_failures"
				}
			}
			_ = c.saveRollout(ctx, all, phase, state, result)
			mu.Unlock()
			return
		}
		phase := "rollout_continued"
		if view.Rollout.Mode == "canary" && batch[0].ID == view.Rollout.Children[0] {
			phase = "canary_started"
		}
		if err := c.saveRollout(ctx, view, phase, "running", ""); err != nil {
			mu.Unlock()
			return
		}
		for _, child := range batch {
			if err := c.repo.AdvanceNodeUpdate(ctx, child.ID, "preflight", 1, "", "", "", "", "", false); err != nil {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		var group sync.WaitGroup
		for _, child := range batch {
			group.Add(1)
			go func(child NodeUpdateOperation) {
				defer group.Done()
				childCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				apply := c.rolloutApply
				if apply == nil {
					apply = c.updateServiceNow
				}
				_, err := apply(childCtx, Request{NodeID: child.NodeID, OperationID: child.ID, Channel: child.RequestedChannel, Policy: child.UpdatePolicy, Version: child.RequestedVersion})
				if err != nil {
					if errors.Is(err, operationapp.ErrLeaseHeld) || errors.Is(err, operationapp.ErrLeaseLost) || errors.Is(err, ErrCommandOutcomeUnknown) {
						return
					}
					stored, lookupErr := c.repo.NodeUpdateOperation(context.WithoutCancel(childCtx), child.ID)
					if lookupErr == nil && !operationapp.Terminal(operationapp.StateForPhase(stored.Phase)) {
						_ = c.repo.AdvanceNodeUpdate(context.WithoutCancel(childCtx), child.ID, "failed", 100, "", "", "", err.Error(), "", true)
					}
				}
			}(child)
		}
		group.Wait()
	}
}

// Reconcile standalone operations and interrupted children before scheduling
// untouched rollout children. Recovery never blindly repeats binary replacement.
func (c Controller) RecoverRollouts(ctx context.Context) error {
	if c.recoveryLifetime != nil {
		c.recoveryLifetime.Store(&controllerRecoveryContext{ctx})
	}
	if _, err := operationapp.RepairOrphanLocks(ctx, c.repo.db); err != nil {
		return err
	}
	items, err := operationapp.ListActive(ctx, c.repo.db)
	if err != nil {
		return err
	}
	for _, op := range items {
		if legacyMaintenanceType(op.Type) {
			if err := c.recoverLegacyMaintenance(ctx, op); err != nil {
				return err
			}
			continue
		}
		if op.Type == "node_update" || op.Type == "node_rollback" {
			update, err := decodeNodeUpdate(op)
			if err != nil {
				return err
			}
			// Draft/queued rollout children are owned by the parent scheduler.
			if update.RolloutID != "" && update.Phase == "queued" {
				continue
			}
			if err := c.recoverNodeMaintenance(ctx, op, update); err != nil {
				return err
			}
		}
	}
	for _, op := range items {
		if op.Type != "node_rollout" || operationapp.Terminal(op.State) {
			continue
		}
		view, err := c.Rollout(ctx, op.ID)
		if err != nil {
			return err
		}
		if !view.Rollout.Confirmed {
			continue
		}
		c.launchRollout(op.ID)
	}
	return nil
}

func (c Controller) RetryRollout(ctx context.Context, id string) (RolloutView, error) {
	view, err := c.Rollout(ctx, id)
	if err != nil {
		return view, err
	}
	if !operationapp.Terminal(view.Operation.State) {
		return view, fmt.Errorf("rollout must finish before failed-only retry")
	}
	req := RolloutRequest{Mode: view.Rollout.Mode, Concurrency: view.Rollout.Concurrency, CanaryCount: view.Rollout.CanaryCount, Confirm: true}
	previous := map[int64]string{}
	for _, child := range view.Children {
		if child.Phase == "failed" || child.Phase == "rolled_back" {
			req.NodeIDs = append(req.NodeIDs, child.NodeID)
			previous[child.NodeID] = child.PreviousVersion
		}
	}
	if req.CanaryCount > len(req.NodeIDs) {
		req.CanaryCount = len(req.NodeIDs)
	}
	if err := validateRollout(&req); err != nil {
		return RolloutView{}, err
	}
	return c.persistRollout(ctx, req, view.Rollout.ResolvedTarget, previous, id)
}
