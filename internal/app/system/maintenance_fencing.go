package system

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"strconv"
	"time"
)

func (s *MaintenanceOperationStore) AcquireExecution(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	if s.latest.ID != id || !s.latest.Running {
		return fmt.Errorf("active Panel operation required")
	}
	if s.executionLease != nil && s.executionLease.OperationID == id {
		err := operationapp.CheckExecutorLease(operationapp.WithExecutorLease(ctx, *s.executionLease), s.db, id)
		if !errors.Is(err, operationapp.ErrLeaseLost) {
			return err
		}
		// An expired executor must acquire a new generation; retaining the old
		// in-memory lease would prevent every later recovery poll from progressing.
		s.executionLease = nil
	}
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return err
	}
	lease, err := operationapp.AcquireExecutorLease(ctx, s.db, s.dialect, id, "panel-"+hex.EncodeToString(identity[:]), 45*time.Second)
	if err != nil {
		return err
	}
	s.executionLease = &lease
	go s.maintainExecution(lease)
	return nil
}

func (s *MaintenanceOperationStore) maintainExecution(lease operationapp.ExecutorLease) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		current := s.latest
		valid := s.executionLease != nil && s.executionLease.ExecutorID == lease.ExecutorID && s.executionLease.Generation == lease.Generation
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if !valid || current.ID != lease.OperationID || !current.Running {
			_ = operationapp.ReleaseExecutorLease(ctx, s.db, lease)
			cancel()
			return
		}
		// The watchdog cannot renew an abandoned installer forever. The resource
		// reservation stays in DB after the bounded executor stops renewing.
		if current.StartedAtNanos > 0 && time.Since(time.Unix(0, current.StartedAtNanos)) > 4*time.Minute {
			cancel()
			return
		}
		err := operationapp.RenewExecutorLease(ctx, s.db, lease, 45*time.Second)
		cancel()
		if err != nil {
			return
		}
	}
}

func (s *MaintenanceOperationStore) FencedArgs(id string, args []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return args, nil
	}
	if s.executionLease == nil || s.executionLease.OperationID != id {
		return nil, operationapp.ErrLeaseLost
	}
	lease := *s.executionLease
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := operationapp.CheckExecutorLease(operationapp.WithExecutorLease(ctx, lease), s.db, id); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("Panel command required")
	}
	digest := sha256.Sum256([]byte(id + "|" + args[0]))
	result := append([]string(nil), args...)
	return append(result, "--fence-operation-id", id, "--executor-id", lease.ExecutorID, "--lease-generation", strconv.FormatInt(lease.Generation, 10), "--resource-generation", strconv.FormatInt(lease.ResourceGeneration, 10), "--command-id", "panel-command-"+hex.EncodeToString(digest[:16])), nil
}
