package nodeagent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareAnyConnectInboundWritesOwnedSecretsAndUserPolicy(t *testing.T) {
	original := anyConnectCommand
	anyConnectCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestAnyConnectOcpasswdHelper", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_OCPASSWD_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { anyConnectCommand = original })
	server := New(Config{DataDir: t.TempDir()})
	prepared, err := server.prepareAnyConnectInbound(anyConnectRuntimeInbound{Tag: "ac-main", Port: 443, Settings: map[string]any{"ipv4_pool_cidr": "10.71.0.0/24", "server_certificate": "CERT", "server_key": "PRIVATE"}, Users: []anyConnectRuntimeUser{{UserID: 9, Username: "alice", Password: "secret", Status: "active", IPv4Address: "10.71.0.9", DeviceLimit: 1}}}, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(prepared.Files.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "secret") || strings.Contains(string(config), "PRIVATE") {
		t.Fatalf("secret leaked into config: %s", config)
	}
	userConfig, err := os.ReadFile(filepath.Join(prepared.Files.UserConfigDir, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userConfig), "max-same-clients = 1") || !strings.Contains(string(userConfig), "explicit-ipv4 = 10.71.0.9") {
		t.Fatalf("unexpected user config: %s", userConfig)
	}
	info, err := os.Stat(prepared.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("password mode=%o", info.Mode().Perm())
	}
}

func TestAnyConnectSessionPolicyPreservesAllEnforcementFields(t *testing.T) {
	limit := int64(50 * 1024 * 1024)
	expire := int64(2_000_000_000)
	policy := anyConnectSessionPolicy(anyConnectRuntimeUser{Status: "active", UsedTraffic: 11, DataLimit: &limit, Expire: &expire, DeviceLimit: 3, IPLimit: 2, UploadSpeedLimit: 1234, DownloadSpeedLimit: 5678, UsageCoefficient: 1.5, InboundCoefficient: 2})
	if policy.DataLimit != limit || policy.Expire != expire || policy.DeviceLimit != 3 || policy.IPLimit != 2 || policy.UploadSpeedLimit != 1234 || policy.DownloadSpeedLimit != 5678 || policy.UsageCoefficient != 1.5 || policy.InboundCoefficient != 2 {
		t.Fatalf("AnyConnect enforcement fields were not preserved: %+v", policy)
	}
}
func TestAnyConnectOcpasswdHelper(t *testing.T) {
	if os.Getenv("GO_WANT_ANYCONNECT_OCPASSWD_HELPER") != "1" {
		return
	}
	_, _ = io.ReadAll(os.Stdin)
	args := os.Args
	separator := 0
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	args = args[separator+1:]
	if len(args) != 5 || args[0] != "-c" || args[2] != "-g" || args[3] != "antimage" {
		os.Exit(2)
	}
	path := args[1]
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(3)
	}
	_, _ = file.WriteString(args[4] + ":antimage:hashed\n")
	_ = file.Close()
	os.Exit(0)
}
