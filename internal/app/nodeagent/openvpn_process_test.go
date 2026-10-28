package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartOpenVPNInboundPreservesProcessForIdenticalConfig(t *testing.T) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldTerminate := openVPNTerminateProcess
	oldStartupGrace := openVPNStartupGrace
	t.Cleanup(func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNTerminateProcess = oldTerminate
		openVPNStartupGrace = oldStartupGrace
	})
	openVPNLookPath = func(string) (string, error) { return os.Args[0], nil }
	openVPNStartupGrace = 100 * time.Millisecond
	openVPNTerminateProcess = func(process *os.Process) error { return process.Kill() }
	var starts atomic.Int32
	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, name, "-test.run=TestOpenVPNHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}

	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllOpenVPNRuntimes)
	configPath := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(configPath, []byte("config-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.startOpenVPNInbound("openvpn-idempotent", configPath); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	first := server.openVPNRuntimes["openvpn-idempotent"]
	firstPID := first.cmd.Process.Pid
	server.mu.Unlock()

	if err := server.startOpenVPNInbound("openvpn-idempotent", configPath); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	secondPID := server.openVPNRuntimes["openvpn-idempotent"].cmd.Process.Pid
	server.mu.Unlock()
	if starts.Load() != 1 || secondPID != firstPID {
		t.Fatalf("identical config restarted OpenVPN: starts=%d first_pid=%d second_pid=%d", starts.Load(), firstPID, secondPID)
	}

	if err := os.WriteFile(configPath, []byte("config-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.startOpenVPNInbound("openvpn-idempotent", configPath); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	thirdPID := server.openVPNRuntimes["openvpn-idempotent"].cmd.Process.Pid
	server.mu.Unlock()
	if starts.Load() != 2 || thirdPID == firstPID {
		t.Fatalf("changed config did not replace OpenVPN: starts=%d first_pid=%d third_pid=%d", starts.Load(), firstPID, thirdPID)
	}
}

func TestOpenVPNUserOnlyMutationUpdatesAuthWithoutRestart(t *testing.T) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldTerminate := openVPNTerminateProcess
	oldStartupGrace := openVPNStartupGrace
	t.Cleanup(func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNTerminateProcess = oldTerminate
		openVPNStartupGrace = oldStartupGrace
	})
	openVPNLookPath = func(string) (string, error) { return os.Args[0], nil }
	openVPNStartupGrace = 100 * time.Millisecond
	openVPNTerminateProcess = func(process *os.Process) error { return process.Kill() }
	var starts atomic.Int32
	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, name, "-test.run=TestOpenVPNHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}

	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllOpenVPNRuntimes)
	inbound := openVPNRuntimeInbound{
		Tag:       "openvpn-user-mutation",
		Port:      1194,
		Transport: "udp",
		Settings: map[string]any{
			"ipv4_pool_cidr":     "10.66.0.0/16",
			"ca":                 "TEST-CA",
			"server_certificate": "TEST-CERT",
			"server_key":         "TEST-KEY",
			"accounting_enabled": false,
		},
		Users: []openVPNRuntimeUser{{
			UserID: 1, Username: "alice", VPNUsername: "alice", Password: "alice-secret", Status: "active",
		}},
	}

	applyUsers := func() (string, string) {
		t.Helper()
		configPath, err := server.prepareOpenVPNInbound(inbound)
		if err != nil {
			t.Fatal(err)
		}
		config, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.startOpenVPNInbound(inbound.Tag, configPath); err != nil {
			t.Fatal(err)
		}
		return configPath, string(config)
	}
	sh := ""
	if runtime.GOOS != "windows" {
		sh, _ = exec.LookPath("sh")
	}
	assertAuth := func(root, username, password string, wantAllowed bool) {
		t.Helper()
		passwordHash := sha256.Sum256([]byte(username + "\x00" + password))
		wantRecord := hex.EncodeToString(passwordHash[:])
		credentials, err := os.ReadFile(filepath.Join(root, "credentials.sha256"))
		if err != nil {
			t.Fatal(err)
		}
		hasRecord := false
		for _, line := range strings.Split(string(credentials), "\n") {
			if strings.HasPrefix(line, wantRecord+"\t") {
				hasRecord = true
				break
			}
		}
		if hasRecord != wantAllowed {
			t.Fatalf("credential record for %q present=%v, want %v", username, hasRecord, wantAllowed)
		}
		if sh == "" {
			return
		}
		authFile := filepath.Join(t.TempDir(), "auth.txt")
		if err := os.WriteFile(authFile, []byte(username+"\n"+password+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		err = exec.Command(sh, filepath.Join(root, "auth.sh"), authFile).Run()
		if (err == nil) != wantAllowed {
			t.Fatalf("auth for %q allowed=%v, want %v (err=%v)", username, err == nil, wantAllowed, err)
		}
	}
	currentPID := func() int {
		t.Helper()
		server.mu.Lock()
		defer server.mu.Unlock()
		runtimeProcess := server.openVPNRuntimes[inbound.Tag]
		if runtimeProcess == nil || runtimeProcess.cmd == nil || runtimeProcess.cmd.Process == nil {
			t.Fatal("OpenVPN runtime is missing")
		}
		select {
		case <-runtimeProcess.done:
			t.Fatal("OpenVPN runtime exited during user-only mutation")
		default:
		}
		return runtimeProcess.cmd.Process.Pid
	}

	configPath, firstConfig := applyUsers()
	root := filepath.Dir(configPath)
	firstPID := currentPID()
	assertAuth(root, "alice", "alice-secret", true)

	// Adding a user changes only the auth/CCD inputs. The OpenVPN server
	// configuration keeps the same stable paths, so applying it must preserve
	// the process and its existing client sessions.
	inbound.Users = append(inbound.Users, openVPNRuntimeUser{
		UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-v1", Status: "active",
	})
	configPath, addedConfig := applyUsers()
	if addedConfig != firstConfig || currentPID() != firstPID || starts.Load() != 1 {
		t.Fatalf("adding a user restarted OpenVPN or changed daemon config: starts=%d pid=%d want=%d", starts.Load(), currentPID(), firstPID)
	}
	root = filepath.Dir(configPath)
	assertAuth(root, "alice", "alice-secret", true)
	assertAuth(root, "bob", "bob-v1", true)

	// A password update takes effect for new authentication without restarting
	// the daemon or invalidating Alice's established session.
	inbound.Users[1].Password = "bob-v2"
	configPath, updatedConfig := applyUsers()
	if updatedConfig != firstConfig || currentPID() != firstPID || starts.Load() != 1 {
		t.Fatalf("updating a user restarted OpenVPN or changed daemon config: starts=%d pid=%d want=%d", starts.Load(), currentPID(), firstPID)
	}
	root = filepath.Dir(configPath)
	assertAuth(root, "alice", "alice-secret", true)
	assertAuth(root, "bob", "bob-v1", false)
	assertAuth(root, "bob", "bob-v2", true)

	// Removing Bob revokes new authentication while Alice and the same daemon
	// process remain live.
	inbound.Users = inbound.Users[:1]
	configPath, removedConfig := applyUsers()
	if removedConfig != firstConfig || currentPID() != firstPID || starts.Load() != 1 {
		t.Fatalf("removing a user restarted OpenVPN or changed daemon config: starts=%d pid=%d want=%d", starts.Load(), currentPID(), firstPID)
	}
	root = filepath.Dir(configPath)
	assertAuth(root, "alice", "alice-secret", true)
	assertAuth(root, "bob", "bob-v2", false)
}

func TestApplyNativeRuntimePreservesOpenVPNProcessForUserOnlySync(t *testing.T) {
	oldLookPath := openVPNLookPath
	oldNetworkRun := openVPNNetworkRun
	oldNetworkLookPath := openVPNNetworkLookPath
	oldNATGOOS := openVPNNATGOOS
	oldCommand := openVPNCommandContext
	oldTerminate := openVPNTerminateProcess
	oldStartupGrace := openVPNStartupGrace
	t.Cleanup(func() {
		openVPNLookPath = oldLookPath
		openVPNNetworkRun = oldNetworkRun
		openVPNNetworkLookPath = oldNetworkLookPath
		openVPNNATGOOS = oldNATGOOS
		openVPNCommandContext = oldCommand
		openVPNTerminateProcess = oldTerminate
		openVPNStartupGrace = oldStartupGrace
	})
	openVPNLookPath = func(string) (string, error) { return os.Args[0], nil }
	openVPNNetworkLookPath = func(name string) (string, error) { return name, nil }
	// This test verifies process preservation; don't touch the host's firewall.
	openVPNNATGOOS = "linux"
	openVPNNetworkRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, nil
	}
	openVPNStartupGrace = 100 * time.Millisecond
	openVPNTerminateProcess = func(process *os.Process) error { return process.Kill() }
	var starts atomic.Int32
	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, name, "-test.run=TestOpenVPNHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}

	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllOpenVPNRuntimes)
	inbound := openVPNRuntimeInbound{
		Tag: "openvpn-full-sync", Port: 1194, Transport: "udp", TunnelPort: 29986,
		Settings: map[string]any{
			"ipv4_pool_cidr":     "10.66.0.0/16",
			"ca":                 "TEST-CA",
			"server_certificate": "TEST-CERT",
			"server_key":         "TEST-KEY",
			"accounting_enabled": false,
			"tproxy_enabled":     false,
			"nat_enabled":        false,
		},
		Users: []openVPNRuntimeUser{{UserID: 1, Username: "alice", VPNUsername: "alice", Password: "alice-secret", Status: "active"}},
	}
	apply := func() {
		t.Helper()
		raw, err := json.Marshal(nativeRuntimePayload{OpenVPNInbounds: []openVPNRuntimeInbound{inbound}})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.applyNativeRuntime(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	processPID := func() int {
		t.Helper()
		server.mu.Lock()
		defer server.mu.Unlock()
		process := server.openVPNRuntimes[inbound.Tag]
		if process == nil || process.cmd == nil || process.cmd.Process == nil {
			t.Fatal("full native sync did not register the OpenVPN process")
		}
		select {
		case <-process.done:
			t.Fatal("OpenVPN process exited during full native sync")
		default:
		}
		return process.cmd.Process.Pid
	}

	apply()
	firstPID := processPID()
	inbound.Users = append(inbound.Users, openVPNRuntimeUser{
		UserID: 2, Username: "bob", VPNUsername: "bob", Password: "bob-secret", Status: "active",
	})
	apply()
	if got := processPID(); got != firstPID || starts.Load() != 1 {
		t.Fatalf("user-only full sync restarted OpenVPN: starts=%d pid=%d want=%d", starts.Load(), got, firstPID)
	}
}

func TestStartOpenVPNInbound(t *testing.T) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
	}()

	openVPNLookPath = func(string) (string, error) {
		return os.Args[0], nil
	}

	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(
			ctx,
			name,
			"-test.run=TestOpenVPNHelperProcess",
			"--",
		)
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}

	server := New(Config{DataDir: t.TempDir()})

	configPath := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(configPath, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := server.startOpenVPNInbound("openvpn-main", configPath); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.stopAllOpenVPNRuntimes)

	server.mu.Lock()
	runtimeProcess := server.openVPNRuntimes["openvpn-main"]
	server.mu.Unlock()

	if runtimeProcess == nil ||
		runtimeProcess.cmd == nil ||
		runtimeProcess.cmd.Process == nil {
		t.Fatal("openvpn process was not registered")
	}

	if err := server.stopOpenVPNInbound("openvpn-main"); err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	_, exists := server.openVPNRuntimes["openvpn-main"]
	server.mu.Unlock()

	if exists {
		t.Fatal("openvpn process was not removed")
	}
}

func TestStartOpenVPNInboundRequiresBinary(t *testing.T) {
	oldLookPath := openVPNLookPath
	defer func() { openVPNLookPath = oldLookPath }()

	openVPNLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	server := New(Config{DataDir: t.TempDir()})

	if err := server.startOpenVPNInbound("openvpn-main", "/tmp/server.conf"); err == nil {
		t.Fatal("expected missing openvpn executable error")
	}
}

func TestOpenVPNHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OPENVPN_HELPER") != "1" {
		return
	}

	if runtime.GOOS == "windows" {
		for {
			time.Sleep(time.Second)
		}
	}

	select {}
}

func TestStartOpenVPNInboundRejectsEarlyExit(
	t *testing.T,
) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldGrace := openVPNStartupGrace

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNStartupGrace = oldGrace
	}()

	openVPNStartupGrace = 500 * time.Millisecond

	openVPNLookPath = func(
		string,
	) (string, error) {
		return os.Args[0], nil
	}

	openVPNCommandContext = func(
		ctx context.Context,
		name string,
		args ...string,
	) *exec.Cmd {
		cmd := exec.CommandContext(
			ctx,
			name,
			"-test.run=TestOpenVPNEarlyExitHelperProcess",
			"--",
		)

		cmd.Env = append(
			os.Environ(),
			"GO_WANT_OPENVPN_EARLY_EXIT=1",
		)

		return cmd
	}

	server := New(
		Config{
			DataDir: t.TempDir(),
		},
	)

	configPath := filepath.Join(
		t.TempDir(),
		"server.conf",
	)

	if err := os.WriteFile(
		configPath,
		[]byte("test"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	err := server.startOpenVPNInbound(
		"openvpn-early-exit",
		configPath,
	)

	if err == nil {
		t.Fatal(
			"expected early OpenVPN exit error",
		)
	}

	server.mu.Lock()
	_, exists :=
		server.openVPNRuntimes["openvpn-early-exit"]
	server.mu.Unlock()

	if exists {
		t.Fatal(
			"early-exited process remained registered",
		)
	}
}

func TestOpenVPNEarlyExitHelperProcess(
	t *testing.T,
) {
	if os.Getenv(
		"GO_WANT_OPENVPN_EARLY_EXIT",
	) != "1" {
		return
	}

	os.Exit(23)
}
