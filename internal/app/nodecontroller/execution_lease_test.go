package nodecontroller

import (
	"context"
	"errors"
	"sync"
	"testing"

	operationapp "github.com/antimage/antimage/internal/app/operations"
)

func TestIndependentControllersShareOneExecutorLease(t *testing.T) {
	for _, phase := range []string{"downloading", "installing", "rolling_back", "preflight", "restoring"} {
		t.Run(phase, func(t *testing.T) {
			first := rolloutTestController(t)
			second := NewController(NewRepository(first.repo.db, "sqlite"))
			ctx := context.Background()
			op := NodeUpdateOperation{ID: "shared-execution", NodeID: 1, Phase: phase}
			if err := first.repo.StartNodeUpdate(ctx, op); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			finish := make(chan struct{})
			results := make(chan error, 2)
			var group sync.WaitGroup
			for _, controller := range []Controller{first, second} {
				group.Add(1)
				go func(controller Controller) {
					defer group.Done()
					<-start
					workerCtx, release, err := controller.beginNodeExecution(ctx, op.ID)
					if err == nil {
						err = controller.requireNodeExecutor(workerCtx, op.ID)
					}
					results <- err
					if release != nil {
						<-finish
						release()
					}
				}(controller)
			}
			close(start)
			owners, blocked := 0, 0
			for attempt := 0; attempt < 2; attempt++ {
				err := <-results
				if err == nil {
					owners++
				} else if errors.Is(err, operationapp.ErrLeaseHeld) {
					blocked++
				} else {
					close(finish)
					group.Wait()
					t.Fatal(err)
				}
			}
			close(finish)
			group.Wait()
			if owners != 1 || blocked != 1 {
				t.Fatalf("owners=%d blocked=%d", owners, blocked)
			}
		})
	}
}
