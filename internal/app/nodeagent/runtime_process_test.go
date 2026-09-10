package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestStartRuntimeDoesNotBindXrayProcessToRequestContext(t *testing.T) {
	previousCommand := xrayCommandContext
	defer func() { xrayCommandContext = previousCommand }()

	var processContext context.Context
	xrayCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		processContext = ctx
		allArgs := append([]string{"-test.run=TestRuntimeHelperProcess", "--", "xray"}, args...)
		cmd := exec.CommandContext(ctx, name, allArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_RUNTIME_HELPER_PROCESS=1")
		return cmd
	}

	dataDir := t.TempDir()
	xrayPath := filepath.Join(dataDir, "xray")
	if err := os.WriteFile(xrayPath, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	server := New(Config{DataDir: dataDir, XrayPath: xrayPath, XrayAssetsDir: dataDir})
	requestContext, cancel := context.WithCancel(context.Background())
	_, err := server.StartRuntime(requestContext, &nodev1.RuntimeConfigRequest{
		ConfigJson: `{"inbounds":[],"outbounds":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.stopRuntime() })
	if processContext == nil {
		t.Fatal("xray command was not started")
	}

	cancel()
	select {
	case <-processContext.Done():
		t.Fatal("xray process context was cancelled with the request")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_HELPER_PROCESS") != "1" {
		return
	}
	if runtime.GOOS == "windows" {
		select {}
	}
	select {}
}
