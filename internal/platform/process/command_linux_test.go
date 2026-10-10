//go:build linux

package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCancellationTerminatesParentAndChildProcessGroup(t *testing.T) {
	// Adopt/reap the deliberate test grandchild so this test does not depend on
	// the host/container PID 1's reaping policy. This does not mask live children:
	// Wait4 only succeeds if cancellation actually terminated that child.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pids")
	cmd := CommandContext(ctx, "sh", "-c", `sleep 600 & printf '%s %s' "$$" "$!" > "$1"; wait`, "helper", pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	parent, child := 0, 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			_, _ = fmt.Sscan(string(data), &parent, &child)
		}
		if parent > 0 && child > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if parent != cmd.Process.Pid || child <= 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatal("helper did not report actual parent/child PIDs")
	}
	defer func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(child, &status, syscall.WNOHANG, nil)
	}()
	started := time.Now()
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("hanging helper reported success after cancellation")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancellation exceeded bounded wait")
	}
	var status syscall.WaitStatus
	for time.Now().Before(deadline) {
		pid, err := syscall.Wait4(child, &status, syscall.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pid == child {
			if !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("child exit was not cancellation: %v", status)
			}
			if err := syscall.Kill(parent, 0); err != syscall.ESRCH {
				t.Fatalf("parent PID still exists: %v", err)
			}
			if err := syscall.Kill(child, 0); err != syscall.ESRCH {
				t.Fatalf("child/zombie PID still exists: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child survived cancellation or was not reaped")
}
