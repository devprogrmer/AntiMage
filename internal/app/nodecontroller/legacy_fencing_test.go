package nodecontroller

import (
	"context"
	"errors"
	"testing"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLegacyCommandsConflictWithNodeUpdateAndEachOther(t *testing.T) {
	for _, action := range []string{"core_update", "geo_update", "core_restart", "sync_config", "node_restart", "stop_runtime", "host_reboot"} {
		t.Run(action, func(t *testing.T) {
			c := rolloutTestController(t)
			ctx := context.Background()
			count := 0
			var oldContext context.Context
			err := c.executeLegacyNodeCommand(ctx, 1, "legacy-"+action, action, func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
				oldContext = context.WithoutCancel(worker)
				count++
				if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "competing-update", NodeID: 1}); err == nil {
					t.Fatal("Node update bypassed legacy resource reservation")
				}
				return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: fence.OperationId, CommandId: fence.CommandId, ResourceId: fence.ResourceId, ResourceGeneration: fence.ResourceGeneration, LeaseGeneration: fence.LeaseGeneration, Runtime: &nodev1.RuntimeState{RuntimeStopVerified: action == "stop_runtime"}}, nil
			}, nil)
			if err != nil || count != 1 {
				t.Fatalf("legacy command: count=%d error=%v", count, err)
			}
			if action == "node_restart" || action == "host_reboot" {
				stored, err := operationapp.Get(ctx, c.repo.db, "legacy-"+action)
				if err != nil || stored.Phase != "waiting_for_reconnect" || stored.CompletedAt != nil {
					t.Fatalf("enqueue counted as completed execution: %+v %v", stored, err)
				}
				if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "new-update", NodeID: 1}); err == nil {
					t.Fatal("unverified restart released ownership")
				}
				return
			}
			if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "new-update", NodeID: 1}); err != nil {
				t.Fatal(err)
			}
			lease, err := operationapp.AcquireExecutorLease(ctx, c.repo.db, "sqlite", "new-update", "new-worker", nodeExecutorLeaseDuration)
			if err != nil || lease.ResourceGeneration != 2 {
				t.Fatalf("cross-operation generation: %+v %v", lease, err)
			}
			if _, err := operationapp.ExecFencedTarget(oldContext, c.repo.db, "node", "1", `UPDATE nodes SET node_binary_tag='stale-result' WHERE id=1`); !errors.Is(err, operationapp.ErrLeaseLost) {
				t.Fatalf("stale legacy result changed runtime projection: %v", err)
			}
		})
	}
}

func TestLegacyLostACKRetainsLockAndDoesNotRedispatch(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	count := 0
	call := func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		count++
		return nil, context.DeadlineExceeded
	}
	if err := c.executeLegacyNodeCommand(ctx, 1, "lost-core", "core_update", call, nil); !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatal(err)
	}
	stored, err := operationapp.Get(ctx, c.repo.db, "lost-core")
	if err != nil || stored.Phase != "outcome_unknown" || operationapp.Terminal(stored.State) {
		t.Fatalf("unknown outcome not persisted: %+v %v", stored, err)
	}
	if err := c.executeLegacyNodeCommand(ctx, 1, "lost-core", "core_update", call, nil); !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("lost ACK caused blind replay")
	}
	if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "competing", NodeID: 1}); err == nil {
		t.Fatal("unknown outcome released reservation")
	}
}
