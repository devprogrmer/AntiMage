//go:build linux

package nodeagent

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOfflineProcessIdentityTreatsZombieAsExited(t *testing.T) {
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Wait() }()
	pid := strconv.Itoa(child.Process.Pid)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile("/proc/" + pid + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("kernel reaped the short-lived child before it became observable as a zombie")
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			t.Fatalf("invalid child process stat: %q", raw)
		}
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) > 0 && fields[0] == "Z" {
			if identity, err := offlineProcessIdentity(pid); !errors.Is(err, os.ErrNotExist) || identity != "" {
				t.Fatalf("zombie process must be classified as exited: identity=%q err=%v", identity, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %s did not become a zombie", pid)
}
