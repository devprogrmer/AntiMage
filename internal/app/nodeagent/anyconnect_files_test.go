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
	if len(args) < 3 {
		os.Exit(2)
	}
	path := args[len(args)-2]
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(3)
	}
	_, _ = file.WriteString(args[len(args)-1] + ":antimage:hashed\n")
	_ = file.Close()
	os.Exit(0)
}
