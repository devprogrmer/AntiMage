package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestXrayStartupOutlivesExpiredSamplingRequest(t *testing.T) {
	previous := xrayCommandContext
	defer func() { xrayCommandContext = previous }()
	xrayCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, name, "-test.run=^TestRuntimeHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_WANT_RUNTIME_HELPER_PROCESS=1")
		return cmd
	}
	s := New(Config{DataDir: t.TempDir(), XrayPath: os.Args[0]})
	path := filepath.Join(s.cfg.DataDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"inbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stopRuntime() })
	// A quota sampling deadline can expire after the durable cutoff and process
	// spawn. The accepted process must not be killed by that expired request.
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := s.startXray(ctx, path); err != nil {
		t.Fatalf("sampling deadline killed the replacement core: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("test did not expire the request during startup")
	}
	if !s.runtimeState("after sampling deadline").GetStarted() {
		t.Fatal("replacement core was lost")
	}
	previousProcess := s.lastRuntime
	if err := s.startXray(ctx, path); err == nil {
		t.Fatal("an already expired request started another transition")
	}
	if s.lastRuntime != previousProcess {
		t.Fatal("an already expired request replaced the active core")
	}
}
