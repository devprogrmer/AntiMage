package nodeagent

import (
	"context"
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

func TestStartRuntimeRecordsAppliedRevisionAfterSuccessfulNativeRuntime(t *testing.T) {
	server := New(Config{
		DataDir:  t.TempDir(),
		XrayPath: filepath.Join(t.TempDir(), "missing-xray"),
	})

	resp, err := server.StartRuntime(context.Background(), &nodev1.RuntimeConfigRequest{
		ConfigJson:      `{"inbounds":[],"outbounds":[]}`,
		DesiredRevision: 17,
	})
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

	_, err := server.StartRuntime(context.Background(), &nodev1.RuntimeConfigRequest{
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
	})
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
