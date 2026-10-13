package nodeagent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	xrayPath := os.Args[0]
	server := New(Config{DataDir: dataDir, XrayPath: xrayPath, XrayAssetsDir: dataDir})
	requestContext, cancel := context.WithCancel(context.Background())
	_, err := server.applyConfig(requestContext, &nodev1.RuntimeConfigRequest{
		ConfigJson: `{"inbounds":[],"outbounds":[]}`,
	}, "started")
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

func TestStartXrayIfConfigUnchangedPreservesLiveProcess(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xray.json")
	config := []byte(`{"inbounds":[],"outbounds":[]}`)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(config)

	previousCommand := xrayCommandContext
	t.Cleanup(func() { xrayCommandContext = previousCommand })
	var starts int
	xrayCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		starts++
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-xray"), args...)
	}

	server := New(Config{XrayPath: filepath.Join(t.TempDir(), "xray")})
	server.lastRuntime = &exec.Cmd{Process: &os.Process{Pid: 1}}
	runtimeExited := make(chan struct{})
	server.runtimeExited = runtimeExited
	server.activeConfigSHA256 = fmt.Sprintf("%x", digest)
	if err := server.startXrayIfConfigChanged(context.Background(), configPath); err != nil {
		t.Fatalf("identical config on live runtime: %v", err)
	}
	if starts != 0 {
		t.Fatalf("identical config started Xray %d times", starts)
	}

	close(runtimeExited)
	server.lastRuntime = &exec.Cmd{}
	if err := server.startXrayIfConfigChanged(context.Background(), configPath); err == nil {
		t.Fatal("exited runtime was not restarted")
	}
	if starts != 1 {
		t.Fatalf("exited runtime restart attempts=%d, want 1", starts)
	}
}

func TestStartRuntimeRecordsAppliedRevisionAfterSuccessfulNativeRuntime(t *testing.T) {
	server := New(Config{
		DataDir:  t.TempDir(),
		XrayPath: filepath.Join(t.TempDir(), "missing-xray"),
	})

	resp, err := server.applyConfig(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"inbounds":[],"outbounds":[]}`,
		DesiredRevision: 17,
	}, "started")
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetRuntime().GetAppliedRevision(); got != 17 {
		t.Fatalf("applied revision = %d, want 17", got)
	}
}

func TestStartRuntimeDoesNotRecordAppliedRevisionWhenOpenVPNFails(t *testing.T) {
	oldLookPath := openVPNLookPath
	defer func() { openVPNLookPath = oldLookPath }()

	openVPNLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	server := New(Config{
		DataDir:  t.TempDir(),
		XrayPath: filepath.Join(t.TempDir(), "missing-xray"),
	})

	_, err := server.applyConfig(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"inbounds":[],"outbounds":[]}`,
		DesiredRevision: 23,
		OvRuntimeJson: `{
			"inbounds": [{
				"tag": "openvpn-main",
				"port": 1194,
				"transport": "udp",
				"settings": {
					"ipv4_pool_cidr": "10.66.0.0/16",
					"ca": "TEST-CA",
					"server_certificate": "TEST-CERT",
					"server_key": "TEST-KEY",
					"tproxy_enabled": false
				},
				"users": [{
					"user_id": 42,
					"username": "alice",
					"vpn_username": "alice",
					"password": "secret-password",
					"ipv4_address": "10.66.0.2",
					"status": "active"
				}]
			}]
		}`,
	}, "started")
	if err == nil || !strings.Contains(err.Error(), "executable not installed") {
		t.Fatalf("error = %v, want missing OpenVPN executable", err)
	}
	if got := server.runtimeState("check").GetAppliedRevision(); got != 0 {
		t.Fatalf("applied revision = %d, want 0", got)
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
