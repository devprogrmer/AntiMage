package nodecontroller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const nodeExecutorLeaseDuration = 45 * time.Second

// A dispatched command may have committed its side effects even when no usable
// response reaches the controller. This error requires external reconciliation.
var ErrCommandOutcomeUnknown = errors.New("command outcome unknown; reconciliation required")

func (c Controller) beginNodeExecution(ctx context.Context, id string) (context.Context, func(), error) {
	if inherited, ok := operationapp.ExecutorLeaseFromContext(ctx); ok {
		if inherited.OperationID != id {
			return nil, nil, operationapp.ErrLeaseLost
		}
		if err := operationapp.CheckExecutorLease(ctx, c.repo.db, id); err != nil {
			return nil, nil, err
		}
		return ctx, func() {}, nil
	}
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, nil, err
	}
	lease, err := operationapp.AcquireExecutorLease(ctx, c.repo.db, c.repo.dialect, id, "executor-"+hex.EncodeToString(identity[:]), nodeExecutorLeaseDuration)
	if err != nil {
		return nil, nil, err
	}
	workerCtx, cancel := context.WithCancel(operationapp.WithExecutorLease(ctx, lease))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(workerCtx, 3*time.Second)
				err := operationapp.RenewExecutorLease(renewCtx, c.repo.db, lease, nodeExecutorLeaseDuration)
				renewCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return workerCtx, func() {
		cancel()
		<-done
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer releaseCancel()
		// A failed release retains only a bounded executor lease; it does not
		// release the target reservation or reopen a terminal operation.
		_ = operationapp.ReleaseExecutorLease(releaseCtx, c.repo.db, lease)
	}, nil
}

func (c Controller) sendFencedServiceUpdate(ctx context.Context, client *nodeclient.Client, id string, req *nodev1.ServiceUpdateRequest) (*nodev1.RuntimeActionResponse, error) {
	if err := c.requireNodeExecutor(ctx, id); err != nil {
		return nil, err
	}
	lease, ok := operationapp.ExecutorLeaseFromContext(ctx)
	if !ok {
		return nil, operationapp.ErrLeaseLost
	}
	action := req.Action
	if action == "" {
		action = "update"
	}
	req.FenceOperationId = id
	req.LeaseGeneration = lease.Generation
	req.ResourceGeneration = lease.ResourceGeneration
	req.ResourceId = lease.TargetID
	req.CommandId = nodeServiceCommandID(id, action, req.OperationId)
	rpcCtx, rpcCancel := context.WithTimeout(ctx, 30*time.Second)
	defer rpcCancel()
	response, err := client.Runtime().UpdateService(rpcCtx, req)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(rpcCtx.Err(), context.DeadlineExceeded) {
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_ = operationapp.AppendAudit(auditCtx, c.repo.db, id, "watchdog_expired", map[string]any{"command_id": req.CommandId, "error_code": "service_rpc_deadline"})
		auditCancel()
	}
	// Even an accepted response becomes stale if ownership changed in flight.
	if fenceErr := c.validateFencedServiceResult(ctx, id, req, response, err); fenceErr != nil {
		return nil, fenceErr
	}
	return response, nil
}

func (c Controller) validateFencedServiceResult(ctx context.Context, id string, req *nodev1.ServiceUpdateRequest, response *nodev1.RuntimeActionResponse, rpcErr error) error {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := c.requireNodeExecutor(checkCtx, id); err != nil {
		return err
	}
	lease, ok := operationapp.ExecutorLeaseFromContext(ctx)
	if !ok || req.LeaseGeneration != lease.Generation || req.FenceOperationId != id || req.ResourceGeneration != lease.ResourceGeneration || req.ResourceId != lease.TargetID {
		return operationapp.ErrLeaseLost
	}
	if rpcErr != nil {
		return fmt.Errorf("%w: %w", ErrCommandOutcomeUnknown, rpcErr)
	}
	if response == nil || response.OperationId != req.OperationId || response.CommandId != req.CommandId || response.LeaseGeneration != req.LeaseGeneration || response.ResourceGeneration != req.ResourceGeneration || response.ResourceId != req.ResourceId {
		return fmt.Errorf("%w: node returned missing or stale command fencing evidence", ErrCommandOutcomeUnknown)
	}
	return nil
}

func (c Controller) requireNodeExecutor(ctx context.Context, id string) error {
	if err := operationapp.CheckExecutorLease(ctx, c.repo.db, id); err != nil {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = operationapp.AppendAudit(auditCtx, c.repo.db, id, "fencing_rejected", map[string]any{"error_code": "executor_lease_lost"})
		return fmt.Errorf("node operation execution fenced: %w", err)
	}
	return nil
}
