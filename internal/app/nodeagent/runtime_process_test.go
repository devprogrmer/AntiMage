package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	config := []byte("{\"inbounds\":[], \"outbounds\":[]}")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalRuntimeConfigDigest([]byte("{\n  \"outbounds\": [],\n  \"inbounds\": []\n}"))
	if err != nil {
		t.Fatal(err)
	}

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
	server.activeConfigSHA256 = digest
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

func TestStartAnyConnectPreservesLiveProcessUntilServerMaterialChanges(t *testing.T) {
	root := t.TempDir()
	files := anyConnectRuntimeFiles{
		ConfigPath: filepath.Join(root, "ocserv.conf"),
		ServerCert: filepath.Join(root, "server.crt"),
		ServerKey:  filepath.Join(root, "server.key"),
		CACert:     filepath.Join(root, "ca.crt"),
	}
	for path, data := range map[string]string{
		files.ConfigPath: "listen-host = 127.0.0.1\n",
		files.ServerCert: "certificate-v1",
		files.ServerKey:  "private-key-v1",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldLookPath, oldCommand, oldGrace := anyConnectLookPath, anyConnectCommandContext, anyConnectStartupGrace
	var starts int
	anyConnectLookPath = func(string) (string, error) { return os.Args[0], nil }
	anyConnectStartupGrace = 10 * time.Millisecond
	anyConnectCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		starts++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRuntimeHelperProcess", "--", "ocserv")
		cmd.Env = append(os.Environ(), "GO_WANT_RUNTIME_HELPER_PROCESS=1")
		return cmd
	}
	t.Cleanup(func() {
		anyConnectLookPath, anyConnectCommandContext, anyConnectStartupGrace = oldLookPath, oldCommand, oldGrace
	})
	server := New(Config{})
	tag := "anyconnect-runtime-test"
	if err := server.startAnyConnectInbound(preparedAnyConnectRuntime{Tag: tag, Files: files}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.stopAllAnyConnectRuntimes)
	if err := server.startAnyConnectInbound(preparedAnyConnectRuntime{Tag: tag, Files: files}); err != nil {
		t.Fatalf("identical config should preserve the live ocserv process: %v", err)
	}
	if starts != 1 {
		t.Fatalf("identical config started ocserv %d times, want 1", starts)
	}
	if err := os.WriteFile(files.ServerCert, []byte("certificate-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.startAnyConnectInbound(preparedAnyConnectRuntime{Tag: tag, Files: files}); err != nil {
		t.Fatalf("changed certificate should replace ocserv process: %v", err)
	}
	if starts != 2 {
		t.Fatalf("changed certificate started ocserv %d times, want 2", starts)
	}
}

func TestApplyAnyConnectUserSyncPreservesProcessAndPublishesDesiredUsers(t *testing.T) {
	oldCommand, oldCommandContext, oldLookPath, oldGrace := anyConnectCommand, anyConnectCommandContext, anyConnectLookPath, anyConnectStartupGrace
	anyConnectCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestAnyConnectOcpasswdHelper", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "GO_WANT_ANYCONNECT_OCPASSWD_HELPER=1")
		return cmd
	}
	anyConnectLookPath = func(string) (string, error) { return os.Args[0], nil }
	anyConnectStartupGrace = 10 * time.Millisecond
	var starts int
	anyConnectCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		starts++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRuntimeHelperProcess", "--", "ocserv")
		cmd.Env = append(os.Environ(), "GO_WANT_RUNTIME_HELPER_PROCESS=1")
		return cmd
	}
	t.Cleanup(func() {
		anyConnectCommand, anyConnectCommandContext, anyConnectLookPath, anyConnectStartupGrace = oldCommand, oldCommandContext, oldLookPath, oldGrace
	})
	server := New(Config{DataDir: t.TempDir()})
	t.Cleanup(server.stopAllAnyConnectRuntimes)
	inbound := anyConnectRuntimeInbound{
		Tag: "anyconnect-user-sync", Port: 443,
		Settings: map[string]any{"ipv4_pool_cidr": "10.73.0.0/24", "server_certificate": "CERT", "server_key": "KEY"},
		Users:    []anyConnectRuntimeUser{{UserID: 1, Username: "alice", Password: "a1", Status: "active"}},
	}
	prepareAndStart := func() preparedAnyConnectRuntime {
		t.Helper()
		prepared, err := server.prepareAnyConnectInbound(inbound, nativeRuntimeSessionCallback{})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.startAnyConnectInbound(prepared); err != nil {
			t.Fatal(err)
		}
		return prepared
	}
	first := prepareAndStart()
	server.mu.Lock()
	firstPID := server.anyConnectRuntimes[inbound.Tag].cmd.Process.Pid
	server.mu.Unlock()
	assertUsers := func(prepared preparedAnyConnectRuntime, present, absent []string) {
		t.Helper()
		passwords, err := os.ReadFile(prepared.Files.PasswordFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range present {
			if !strings.Contains(string(passwords), value) {
				t.Fatalf("AnyConnect password file missing %q: %s", value, passwords)
			}
		}
		for _, value := range absent {
			if strings.Contains(string(passwords), value) {
				t.Fatalf("AnyConnect password file contains removed %q: %s", value, passwords)
			}
		}
	}
	assertUsers(first, []string{"alice"}, []string{"bob"})
	inbound.Users = append(inbound.Users, anyConnectRuntimeUser{UserID: 2, Username: "bob", Password: "b1", Status: "active"})
	added := prepareAndStart()
	assertUsers(added, []string{"alice", "bob"}, nil)
	addedPasswordFile, err := os.ReadFile(added.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	inbound.Users[0].Password = "a2"
	updated := prepareAndStart()
	assertUsers(updated, []string{"alice", "bob"}, nil)
	updatedPasswordFile, err := os.ReadFile(updated.Files.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(updatedPasswordFile) == string(addedPasswordFile) {
		t.Fatal("updated AnyConnect password did not change the published password file")
	}
	inbound.Users = inbound.Users[:1]
	removed := prepareAndStart()
	assertUsers(removed, []string{"alice"}, []string{"bob"})
	server.mu.Lock()
	lastPID := server.anyConnectRuntimes[inbound.Tag].cmd.Process.Pid
	server.mu.Unlock()
	if starts != 1 || lastPID != firstPID {
		t.Fatalf("AnyConnect user-only sync restarted ocserv: starts=%d first_pid=%d last_pid=%d", starts, firstPID, lastPID)
	}
}

func TestStartPPTPPreservesLiveProcessForIdenticalConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "pptpd.conf")
	if err := os.WriteFile(configPath, []byte("option /etc/ppp/options.pptpd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldSecrets, oldHook := pptpCHAPSecretsPath, pptpIPPreUpHookPath
	oldLookPath, oldCommand, oldHelper := pptpLookPath, pptpCommandContext, pppSessionHelperExecutable
	oldStartup, oldShutdown, oldKill := pptpStartupGrace, pptpShutdownGrace, pptpKillGrace
	pptpCHAPSecretsPath = filepath.Join(root, "chap-secrets")
	pptpIPPreUpHookPath = filepath.Join(root, "ip-pre-up.d", "90-antimage-pptp")
	pptpLookPath = func(string) (string, error) { return os.Args[0], nil }
	pppSessionHelperExecutable = func() (string, error) { return os.Args[0], nil }
	pptpStartupGrace, pptpShutdownGrace, pptpKillGrace = 10*time.Millisecond, 100*time.Millisecond, time.Second
	var starts int
	pptpCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		starts++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPPTPHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_PPTP_HELPER=1")
		return cmd
	}
	t.Cleanup(func() {
		pptpCHAPSecretsPath, pptpIPPreUpHookPath = oldSecrets, oldHook
		pptpLookPath, pptpCommandContext, pppSessionHelperExecutable = oldLookPath, oldCommand, oldHelper
		pptpStartupGrace, pptpShutdownGrace, pptpKillGrace = oldStartup, oldShutdown, oldKill
	})
	server := New(Config{})
	tag := "pptp-runtime-test"
	chapPath := filepath.Join(root, "chap-secrets")
	chapV1 := `"alice" * "password-v1" *`
	if err := server.startPPTPInbound(tag, configPath, chapV1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.stopAllPPTPRuntimes)
	server.mu.Lock()
	firstPID := server.pptpRuntimes[tag].cmd.Process.Pid
	server.mu.Unlock()
	assertCHAP := func(present, absent []string) {
		t.Helper()
		raw, err := os.ReadFile(chapPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range present {
			if !strings.Contains(string(raw), item) {
				t.Fatalf("PPTP CHAP file missing %q: %s", item, raw)
			}
		}
		for _, item := range absent {
			if strings.Contains(string(raw), item) {
				t.Fatalf("PPTP CHAP file still contains %q: %s", item, raw)
			}
		}
	}
	assertCHAP([]string{"alice", "password-v1"}, nil)
	chapAdded := chapV1 + "\n\"bob\" * \"password-b1\" *"
	if err := server.startPPTPInbound(tag, configPath, chapAdded); err != nil {
		t.Fatalf("adding a user should preserve the live pptpd process: %v", err)
	}
	assertCHAP([]string{"alice", "bob", "password-b1"}, nil)
	chapUpdated := `"alice" * "password-v2" *` + "\n" + `"bob" * "password-b1" *`
	if err := server.startPPTPInbound(tag, configPath, chapUpdated); err != nil {
		t.Fatalf("updating a password should preserve the live pptpd process: %v", err)
	}
	assertCHAP([]string{"password-v2", "bob"}, []string{"password-v1"})
	if err := server.startPPTPInbound(tag, configPath, `"alice" * "password-v2" *`); err != nil {
		t.Fatalf("removing a user should preserve the live pptpd process: %v", err)
	}
	if starts != 1 {
		t.Fatalf("PPTP user-only sync started pptpd %d times, want 1", starts)
	}
	server.mu.Lock()
	lastPID := server.pptpRuntimes[tag].cmd.Process.Pid
	server.mu.Unlock()
	if lastPID != firstPID {
		t.Fatalf("PPTP user-only sync changed process identity: first=%d last=%d", firstPID, lastPID)
	}
	assertCHAP([]string{"alice", "password-v2"}, []string{"bob", "password-b1"})
	if err := os.WriteFile(configPath, []byte("option /etc/ppp/options.pptpd\nlogwtmp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.startPPTPInbound(tag, configPath, `"alice" * "password-v2" *`); err != nil {
		t.Fatalf("changed config should replace pptpd process: %v", err)
	}
	if starts != 2 {
		t.Fatalf("changed config started pptpd %d times, want 2", starts)
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
	for {
		time.Sleep(time.Hour)
	}
}
