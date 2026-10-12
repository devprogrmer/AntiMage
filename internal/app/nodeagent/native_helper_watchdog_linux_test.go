//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeDaemonForcedStopTerminatesWorkerGroup(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	for _, fixture := range []struct {
		name    string
		command func(context.Context, string, ...string) *exec.Cmd
		stop    func(*exec.Cmd, chan struct{}) error
	}{
		{"openvpn", openVPNCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopOpenVPNProcess(&openVPNProcess{cmd: cmd, done: done})
		}},
		{"anyconnect", anyConnectCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopAnyConnectProcess(&anyConnectProcess{cmd: cmd, done: done})
		}},
		{"pptp", pptpCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopPPTPProcess(&pptpProcess{cmd: cmd, done: done})
		}},
		{"openvpn-worker", openVPNCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopOpenVPNProcess(&openVPNProcess{cmd: cmd, done: done})
		}},
		{"anyconnect-worker", anyConnectCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopAnyConnectProcess(&anyConnectProcess{cmd: cmd, done: done})
		}},
		{"pptp-worker", pptpCommandContext, func(cmd *exec.Cmd, done chan struct{}) error {
			return stopPPTPProcess(&pptpProcess{cmd: cmd, done: done})
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// A shell worker fixture exercises production process ownership and
			// stop paths without launching any native VPN or touching PPP devices.
			pidFile := filepath.Join(t.TempDir(), "pids")
			script := `trap '' TERM; sleep 600 & printf '%s %s' "$$" "$!" > "$1"; wait`
			if strings.HasSuffix(fixture.name, "-worker") {
				script = `(trap '' TERM INT; exec sleep 600) & printf '%s %s' "$$" "$!" > "$1"; wait`
			}
			cmd := fixture.command(context.Background(), "sh", "-c", script, "daemon-worker", pidFile)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			child := 0
			deadline := time.Now().Add(5 * time.Second)
			for child == 0 && time.Now().Before(deadline) {
				if data, err := os.ReadFile(pidFile); err == nil {
					var parent int
					_, _ = fmt.Sscan(string(data), &parent, &child)
				}
				time.Sleep(10 * time.Millisecond)
			}
			defer func() {
				_ = cmd.Cancel()
				if child > 0 {
					_ = syscall.Kill(child, syscall.SIGKILL)
					var status syscall.WaitStatus
					_, _ = syscall.Wait4(child, &status, syscall.WNOHANG, nil)
				}
			}()
			if child <= 0 {
				t.Fatal("worker did not publish its PID")
			}
			if err := fixture.stop(cmd, done); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var status syscall.WaitStatus
				pid, err := syscall.Wait4(child, &status, syscall.WNOHANG, nil)
				if err != nil {
					t.Fatal(err)
				}
				if pid == child {
					if syscall.Kill(child, 0) != syscall.ESRCH || syscall.Kill(cmd.Process.Pid, 0) != syscall.ESRCH {
						t.Fatal("daemon or worker survived forced stop")
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("daemon worker survived or was not reaped")
		})
	}
}

func TestNativeServiceHelperDeadlineTerminatesActualProcessTree(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	previous := l2TPCommandTimeout
	l2TPCommandTimeout = time.Second
	t.Cleanup(func() { l2TPCommandTimeout = previous })
	pidFile := filepath.Join(t.TempDir(), "pids")
	server := New(Config{DataDir: t.TempDir()})
	started := time.Now()
	err := runL2TPCommand(server, "sh", "-c", `sleep 600 & printf '%s %s' "$$" "$!" > "$1"; wait`, "isolated-helper", pidFile)
	if err == nil || time.Since(started) > 4*time.Second {
		t.Fatalf("unbounded helper or false success: %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var parent, child int
	if _, err := fmt.Sscan(string(data), &parent, &child); err != nil || parent <= 0 || child <= 0 {
		t.Fatalf("missing actual PIDs: %s %v", data, err)
	}
	defer func() {
		_ = syscall.Kill(child, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(child, &status, syscall.WNOHANG, nil)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(child, &status, syscall.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pid == child {
			if !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("child was not killed by deadline: %v", status)
			}
			if syscall.Kill(parent, 0) != syscall.ESRCH || syscall.Kill(child, 0) != syscall.ESRCH {
				t.Fatal("helper process or zombie survived")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper child survived timeout")
}

func TestNativeNetworkHelperDeadlinesTerminateActualProcessTrees(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	for _, fixture := range []struct {
		name string
		run  func(context.Context, string, ...string) ([]byte, error)
	}{
		{"speed-limit", nativeSpeedLimitRun},
		{"openvpn-network", openVPNNetworkRun},
		{"wireguard-runtime", wireGuardRuntimeRun},
		{"wireguard-routing", wireGuardRoutingRun},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			started := time.Now()
			_, err := fixture.run(ctx, "sh", "-c", `sleep 600 & printf '%s %s' "$$" "$!" > "$1"; wait`, "network-helper", pidFile)
			if err == nil || time.Since(started) > 4*time.Second {
				t.Fatalf("unbounded helper or false success: %v", err)
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			var parent, child int
			if _, err := fmt.Sscan(string(data), &parent, &child); err != nil || parent <= 0 || child <= 0 {
				t.Fatalf("missing actual PIDs: %s %v", data, err)
			}
			defer func() {
				_ = syscall.Kill(child, syscall.SIGKILL)
				var status syscall.WaitStatus
				_, _ = syscall.Wait4(child, &status, syscall.WNOHANG, nil)
			}()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var status syscall.WaitStatus
				pid, err := syscall.Wait4(child, &status, syscall.WNOHANG, nil)
				if err != nil {
					t.Fatal(err)
				}
				if pid == child {
					if !status.Signaled() || status.Signal() != syscall.SIGKILL || syscall.Kill(parent, 0) != syscall.ESRCH || syscall.Kill(child, 0) != syscall.ESRCH {
						t.Fatalf("helper tree was not killed and reaped: %v", status)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("helper child survived deadline")
		})
	}
}
