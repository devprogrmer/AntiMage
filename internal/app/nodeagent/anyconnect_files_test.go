package nodeagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	passwordInput, _ := io.ReadAll(os.Stdin)
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
	digest := sha256.Sum256(passwordInput)
	_, _ = file.WriteString(args[4] + ":antimage:" + hex.EncodeToString(digest[:]) + "\n")
	_ = file.Close()
	os.Exit(0)
}

func TestPrepareAnyConnectFailureKeepsPreviouslyPublishedUserState(t *testing.T) {
	oldCommand := anyConnectCommand
	anyConnectCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestAnyConnectOcpasswdHelper", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_OCPASSWD_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { anyConnectCommand = oldCommand })
	server := New(Config{DataDir: t.TempDir()})
	base := anyConnectRuntimeInbound{
		Tag: "ac-atomic", Port: 443,
		Settings: map[string]any{"ipv4_pool_cidr": "10.71.0.0/24", "server_certificate": "CERT", "server_key": "PRIVATE"},
		Users:    []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active", IPv4Address: "10.71.0.8", DeviceLimit: 1}},
	}
	previous, err := server.prepareAnyConnectInbound(base, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	previousPassword, err := os.ReadFile(previous.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	previousCCD, err := os.ReadFile(filepath.Join(previous.Files.UserConfigDir, "alice"))
	if err != nil {
		t.Fatal(err)
	}

	base.Users = append(base.Users, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "b1", Status: "active"})
	previousCommand := anyConnectCommand
	calls := 0
	anyConnectCommand = func(name string, args ...string) *exec.Cmd {
		calls++
		if calls == 2 {
			return exec.Command(filepath.Join(t.TempDir(), "missing-ocpasswd"), args...)
		}
		return previousCommand(name, args...)
	}
	if _, err := server.prepareAnyConnectInbound(base, nativeRuntimeSessionCallback{}); err == nil {
		t.Fatal("password helper failure unexpectedly published the new state")
	}
	currentPassword, err := os.ReadFile(previous.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	currentCCD, err := os.ReadFile(filepath.Join(previous.Files.UserConfigDir, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if string(currentPassword) != string(previousPassword) || string(currentCCD) != string(previousCCD) {
		t.Fatalf("failed user build changed existing state: password=%q ccd=%q", currentPassword, currentCCD)
	}
	if _, err := os.Stat(filepath.Join(previous.Files.UserConfigDir, "bob")); !os.IsNotExist(err) {
		t.Fatalf("failed user build published bob's CCD: %v", err)
	}
}

func TestPublishAnyConnectUserStateRollsBackPartialRename(t *testing.T) {
	root := t.TempDir()
	usersPath := filepath.Join(root, "users")
	passwordPath := filepath.Join(root, "ocpasswd")
	stagedUsers := filepath.Join(root, ".stage", "users")
	stagedPassword := filepath.Join(root, ".stage", "ocpasswd")
	for path, content := range map[string]string{
		filepath.Join(usersPath, "alice"):   "old-ccd",
		passwordPath:                        "old-passwords",
		filepath.Join(stagedUsers, "alice"): "new-ccd",
		stagedPassword:                      "new-passwords",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldRename := anyConnectRename
	anyConnectRename = func(oldPath, newPath string) error {
		if oldPath == stagedPassword && newPath == passwordPath {
			return errors.New("injected password publish failure")
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() { anyConnectRename = oldRename })
	if err := publishAnyConnectUserState(root, passwordPath, usersPath, stagedPassword, stagedUsers); err == nil {
		t.Fatal("injected publish failure was ignored")
	}
	password, err := os.ReadFile(passwordPath)
	if err != nil || string(password) != "old-passwords" {
		t.Fatalf("password rollback = %q, err=%v", password, err)
	}
	ccd, err := os.ReadFile(filepath.Join(usersPath, "alice"))
	if err != nil || string(ccd) != "old-ccd" {
		t.Fatalf("CCD rollback = %q, err=%v", ccd, err)
	}
}

// ocserv creates AF_UNIX sockets and appends a per-worker suffix to
// "socket-file". The kernel caps sun_path at 108 bytes, so a deep data
// directory makes the daemon exit immediately with status 0. AnyConnect
// runtime sockets must therefore live under a short, bounded directory.
func TestAnyConnectRuntimeSocketsStayWithinAFUnixPathLimit(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("very-long-data-directory-segment/", 6))
	server := New(Config{DataDir: deep})
	oldCommand := anyConnectCommand
	anyConnectCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestAnyConnectOcpasswdHelper", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_OCPASSWD_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { anyConnectCommand = oldCommand })
	prepared, err := server.prepareAnyConnectInbound(anyConnectRuntimeInbound{Tag: "ac-socket-limit", Port: 443, Settings: map[string]any{"ipv4_pool_cidr": "10.71.0.0/24", "server_certificate": "CERT", "server_key": "PRIVATE"}, Users: []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "secret", Status: "active"}}}, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	// ocserv adds a per-worker suffix (a dot plus eight hex digits plus a
	// generation digit) to the worker socket path.
	if len(prepared.Files.WorkerSocket)+len(".ffffffff.N") > 107 {
		t.Fatalf("worker socket path %q leaves no room for ocserv's per-worker suffix", prepared.Files.WorkerSocket)
	}
	if len(prepared.Files.ControlSocket) > 107 {
		t.Fatalf("control socket path %q exceeds the AF_UNIX limit", prepared.Files.ControlSocket)
	}
	if prepared.Files.ControlSocket != AnyConnectControlSocketPath(deep, "ac-socket-limit") {
		t.Fatalf("control socket %q does not match the exported helper", prepared.Files.ControlSocket)
	}
	if dir := filepath.Dir(prepared.Files.ControlSocket); strings.HasPrefix(prepared.Files.ConfigPath, dir) {
		t.Fatalf("runtime sockets must not live under the deep data directory: %q", dir)
	}
}
