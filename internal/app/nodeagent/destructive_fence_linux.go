//go:build linux

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"golang.org/x/sys/unix"
)

type nativeCommandJournal struct {
	ResourceGenerations map[string]int64                `json:"command_resource_generations,omitempty"`
	Generation          int64                           `json:"generation"`
	Commands            map[string]string               `json:"commands"`
	Receipts            map[string]nativeCommandReceipt `json:"receipts,omitempty"`
}

type nativeCommandReceipt struct {
	ResourceGeneration int64 `json:"resource_generation"`
	ProcessStartedAt   int64 `json:"process_started_at_unix_nano"`
	StopVerified       bool  `json:"runtime_stop_verified"`
}

type nativeResourceJournal struct {
	Generation int64  `json:"generation"`
	Owner      string `json:"owner_operation_id"`
	ID         string `json:"resource_id"`
}

var nativeFenceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)

func nativeFencingAvailable() bool {
	app := os.Getenv("ANTIMAGE_NODE_APP_DIR")
	if app == "" {
		return false
	}
	info, err := os.Lstat(app)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && unix.Access(app, unix.W_OK) == nil
}

func acceptNativeDestructiveFence(ctx context.Context, fence *nodev1.DestructiveFence) error {
	if fence == nil || !nativeFenceIdentifier.MatchString(fence.OperationId) || !nativeFenceIdentifier.MatchString(fence.CommandId) || !nativeFenceIdentifier.MatchString(fence.ResourceId) || fence.ResourceGeneration <= 0 || fence.LeaseGeneration <= 0 {
		return fmt.Errorf("valid destructive fencing identity required")
	}
	app := os.Getenv("ANTIMAGE_NODE_APP_DIR")
	if app == "" {
		return fmt.Errorf("persistent Node fencing directory must be configured")
	}
	root := filepath.Join(app, ".maintenance-fences")
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private fencing directory unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lock, err := fenceLock(bounded, filepath.Join(root, ".generation.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	resourcePath := filepath.Join(root, "resource.json")
	resource := nativeResourceJournal{ID: fence.ResourceId}
	if err := readFenceJSON(resourcePath, &resource); err != nil && !os.IsNotExist(err) {
		return err
	}
	if resource.ID != fence.ResourceId || fence.ResourceGeneration < resource.Generation || (fence.ResourceGeneration == resource.Generation && resource.Owner != fence.OperationId) {
		return fmt.Errorf("stale destructive resource owner rejected")
	}
	journalPath := filepath.Join(root, "operation-"+fence.OperationId+".json")
	journal := nativeCommandJournal{Commands: map[string]string{}}
	if err := readFenceJSON(journalPath, &journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	if fence.LeaseGeneration < journal.Generation {
		return fmt.Errorf("stale executor generation rejected")
	}
	if journal.Commands[fence.CommandId] == "started" || journal.Commands[fence.CommandId] == "completed" {
		return fmt.Errorf("command already dispatched; reconcile outcome before replay")
	}
	if fence.LeaseGeneration > journal.Generation {
		for id, state := range journal.Commands {
			if state != "started" && state != "completed" {
				delete(journal.Commands, id)
			}
		}
	}
	if journal.Commands == nil {
		journal.Commands = map[string]string{}
	}
	journal.Generation = fence.LeaseGeneration
	journal.Commands[fence.CommandId] = "accepted"
	// Resource ownership is persisted before acceptance, as in the installed CLI.
	// A crash between the records therefore fails closed at the execution check.
	if fence.ResourceGeneration > resource.Generation {
		resource = nativeResourceJournal{Generation: fence.ResourceGeneration, Owner: fence.OperationId, ID: fence.ResourceId}
		if err := persistNativeCommandJournal(root, resourcePath, resource); err != nil {
			return err
		}
	}
	return persistNativeCommandJournal(root, journalPath, journal)
}

func fenceLock(ctx context.Context, path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func readFenceJSON(path string, target any) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	return json.NewDecoder(file).Decode(target)
}

func runNativeDestructiveBoundary(ctx context.Context, fence *nodev1.DestructiveFence, apply func(context.Context) (*nodev1.RuntimeActionResponse, error)) (*nodev1.RuntimeActionResponse, error) {
	root := filepath.Join(os.Getenv("ANTIMAGE_NODE_APP_DIR"), ".maintenance-fences")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private fencing directory unavailable")
	}
	lockCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	execution, err := fenceLock(lockCtx, filepath.Join(root, ".execution.lock"))
	if err != nil {
		return nil, err
	}
	defer execution.Close()
	generation, err := fenceLock(lockCtx, filepath.Join(root, ".generation.lock"))
	if err != nil {
		return nil, err
	}
	defer generation.Close()
	var resource struct {
		Generation int64  `json:"generation"`
		Owner      string `json:"owner_operation_id"`
		ID         string `json:"resource_id"`
	}
	if err := readFenceJSON(filepath.Join(root, "resource.json"), &resource); err != nil {
		return nil, err
	}
	if resource.Generation != fence.ResourceGeneration || resource.Owner != fence.OperationId || resource.ID != fence.ResourceId {
		return nil, fmt.Errorf("stale destructive resource owner rejected")
	}
	journalPath := filepath.Join(root, "operation-"+fence.OperationId+".json")
	var journal nativeCommandJournal
	if err := readFenceJSON(journalPath, &journal); err != nil {
		return nil, err
	}
	if journal.Generation != fence.LeaseGeneration || journal.Commands[fence.CommandId] != "accepted" {
		return nil, fmt.Errorf("stale or already completed destructive command rejected")
	}
	// The full native action is currently one conservative boundary, including
	// installation and restart. A newer acceptance waits until it completes.
	bounded, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	journal.Commands[fence.CommandId] = "started"
	if journal.ResourceGenerations == nil {
		journal.ResourceGenerations = map[string]int64{}
	}
	journal.ResourceGenerations[fence.CommandId] = fence.ResourceGeneration
	if err := persistNativeCommandJournal(root, journalPath, journal); err != nil {
		return nil, err
	}
	response, err := apply(bounded)
	if err != nil {
		return response, err
	}
	journal.Commands[fence.CommandId] = "completed"
	if journal.Receipts == nil {
		journal.Receipts = map[string]nativeCommandReceipt{}
	}
	if response != nil && response.Runtime != nil {
		journal.Receipts[fence.CommandId] = nativeCommandReceipt{ResourceGeneration: fence.ResourceGeneration, ProcessStartedAt: response.Runtime.ProcessStartedAtUnixNano, StopVerified: response.Runtime.RuntimeStopVerified}
	}
	if err := persistNativeCommandJournal(root, journalPath, journal); err != nil {
		return nil, err
	}
	return response, nil
}

// Evidence is read while the generation lock excludes concurrent ownership
// changes. A receipt alone never proves that its runtime is still stopped.
func readNativeCommandEvidence(ctx context.Context, req *nodev1.HealthRequest, state *nodev1.RuntimeState) error {
	if !nativeFenceIdentifier.MatchString(req.OperationId) || !nativeFenceIdentifier.MatchString(req.CommandId) {
		return fmt.Errorf("invalid command evidence identity")
	}
	root := filepath.Join(os.Getenv("ANTIMAGE_NODE_APP_DIR"), ".maintenance-fences")
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := fenceLock(bounded, filepath.Join(root, ".generation.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	var resource nativeResourceJournal
	if err := readFenceJSON(filepath.Join(root, "resource.json"), &resource); err != nil {
		return err
	}
	var journal nativeCommandJournal
	if err := readFenceJSON(filepath.Join(root, "operation-"+req.OperationId+".json"), &journal); err != nil {
		return err
	}
	state.EvidenceOperationId, state.EvidenceCommandId = req.OperationId, req.CommandId
	state.EvidenceResourceGeneration = resource.Generation
	state.CurrentResourceGeneration = resource.Generation
	if resource.Owner != req.OperationId {
		state.EvidenceCommandState = "superseded"
		return nil
	}
	state.EvidenceCommandState = journal.Commands[req.CommandId]
	if dispatched := journal.ResourceGenerations[req.CommandId]; dispatched > 0 && dispatched <= resource.Generation {
		state.EvidenceResourceGeneration = dispatched
	}
	receipt, ok := journal.Receipts[req.CommandId]
	if ok && receipt.ResourceGeneration <= resource.Generation {
		state.EvidenceResourceGeneration = receipt.ResourceGeneration
		state.EvidenceProcessStartedAtUnixNano = receipt.ProcessStartedAt
		state.RuntimeStopVerified = receipt.StopVerified && !state.Started && receipt.ProcessStartedAt == state.ProcessStartedAtUnixNano
	}
	return nil
}

func persistNativeCommandJournal(root, journalPath string, journal any) error {
	temporary, err := os.CreateTemp(root, ".native-command-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err = json.NewEncoder(temporary).Encode(journal); err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary.Name(), journalPath); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
