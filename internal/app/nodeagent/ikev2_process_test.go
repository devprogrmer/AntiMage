package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallIKEv2SystemConfigReportsOnlyEffectiveChanges(t *testing.T) {
	root := t.TempDir()
	old := [5]string{ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir, l2TPIPSecConfigPath, l2TPIPSecSecretsPath}
	ikev2CACertDir = filepath.Join(root, "system", "cacerts")
	ikev2ServerCertDir = filepath.Join(root, "system", "certs")
	ikev2ServerKeyDir = filepath.Join(root, "system", "private")
	l2TPIPSecConfigPath = filepath.Join(root, "system", "ipsec.conf")
	l2TPIPSecSecretsPath = filepath.Join(root, "system", "ipsec.secrets")
	t.Cleanup(func() {
		ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir = old[0], old[1], old[2]
		l2TPIPSecConfigPath, l2TPIPSecSecretsPath = old[3], old[4]
	})

	sources := filepath.Join(root, "generated")
	if err := os.MkdirAll(sources, 0700); err != nil {
		t.Fatal(err)
	}
	input := map[string]string{
		"ipsec.conf":    "conn antimage-one\n",
		"ipsec.secrets": "@server : RSA antimage-one.pem\n",
		"ca.pem":        "CA bytes",
		"server.pem":    "certificate bytes",
		"server.key":    "private key bytes",
	}
	for name, content := range input {
		if err := os.WriteFile(filepath.Join(sources, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtime := preparedIKEv2Runtime{Tag: "ikev2-one", Files: ikev2RuntimeFiles{
		IPSecConfig: filepath.Join(sources, "ipsec.conf"),
		IPSecSecret: filepath.Join(sources, "ipsec.secrets"),
		CACert:      filepath.Join(sources, "ca.pem"), CAName: "antimage-ikev2-ca.pem",
		ServerCert: filepath.Join(sources, "server.pem"), CertName: "antimage-ikev2-server.pem",
		ServerKey: filepath.Join(sources, "server.key"), KeyName: "antimage-ikev2-server.key",
	}}

	changed, err := installIKEv2SystemConfig([]preparedIKEv2Runtime{runtime})
	if err != nil || !changed {
		t.Fatalf("first install changed=%v err=%v, want changed", changed, err)
	}
	changed, err = installIKEv2SystemConfig([]preparedIKEv2Runtime{runtime})
	if err != nil || changed {
		t.Fatalf("identical install changed=%v err=%v, want no change", changed, err)
	}

	if err := os.WriteFile(runtime.Files.IPSecSecret, []byte("@server : RSA rotated.pem\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = installIKEv2SystemConfig([]preparedIKEv2Runtime{runtime})
	if err != nil || !changed {
		t.Fatalf("secret rotation changed=%v err=%v, want changed", changed, err)
	}

	stale := filepath.Join(ikev2ServerCertDir, "antimage-ikev2-stale.pem")
	if err := os.WriteFile(stale, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = installIKEv2SystemConfig([]preparedIKEv2Runtime{runtime})
	if err != nil || !changed {
		t.Fatalf("stale credential cleanup changed=%v err=%v, want changed", changed, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale credential remains: %v", err)
	}
}

func TestApplyIKEv2RuntimesDoesNotReloadHealthyIdenticalConfig(t *testing.T) {
	root := t.TempDir()
	oldDirs := [5]string{ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir, l2TPIPSecConfigPath, l2TPIPSecSecretsPath}
	ikev2CACertDir = filepath.Join(root, "system", "cacerts")
	ikev2ServerCertDir = filepath.Join(root, "system", "certs")
	ikev2ServerKeyDir = filepath.Join(root, "system", "private")
	l2TPIPSecConfigPath = filepath.Join(root, "system", "ipsec.conf")
	l2TPIPSecSecretsPath = filepath.Join(root, "system", "ipsec.secrets")
	t.Cleanup(func() {
		ikev2CACertDir, ikev2ServerCertDir, ikev2ServerKeyDir = oldDirs[0], oldDirs[1], oldDirs[2]
		l2TPIPSecConfigPath, l2TPIPSecSecretsPath = oldDirs[3], oldDirs[4]
	})

	oldLookPath, oldCommand, oldFirewall := l2TPLookPath, l2TPCommandContext, ikev2FirewallCommand
	var commands [][]string
	l2TPLookPath = func(string) (string, error) { return "/bin/true", nil }
	l2TPCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		commands = append(commands, append([]string(nil), args...))
		return exec.CommandContext(ctx, "/bin/true")
	}
	ikev2FirewallCommand = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() {
		l2TPLookPath, l2TPCommandContext, ikev2FirewallCommand = oldLookPath, oldCommand, oldFirewall
	})

	generated := filepath.Join(root, "generated")
	if err := os.MkdirAll(generated, 0700); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{
		"config": "conn antimage-one\n", "secrets": "@server : RSA antimage-one.pem\n",
		"ca": "CA", "cert": "certificate", "key": "private key",
	}
	paths := make(map[string]string, len(contents))
	for name, content := range contents {
		path := filepath.Join(generated, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	runtime := preparedIKEv2Runtime{Tag: "ikev2-one", Inbound: ikev2RuntimeInbound{Tag: "ikev2-one"}, Files: ikev2RuntimeFiles{
		IPSecConfig: paths["config"], IPSecSecret: paths["secrets"],
		CACert: paths["ca"], CAName: "antimage-ikev2-ca.pem",
		ServerCert: paths["cert"], CertName: "antimage-ikev2-server.pem",
		ServerKey: paths["key"], KeyName: "antimage-ikev2-server.key",
	}}
	server := New(Config{DataDir: filepath.Join(root, "data")})
	if err := server.applyIKEv2Runtimes([]preparedIKEv2Runtime{runtime}); err != nil {
		t.Fatal(err)
	}
	if !hasCommand(commands, "rereadsecrets") || !hasCommand(commands, "reload") {
		t.Fatalf("changed config did not reload strongSwan: %v", commands)
	}
	commands = nil
	if err := server.applyIKEv2Runtimes([]preparedIKEv2Runtime{runtime}); err != nil {
		t.Fatal(err)
	}
	if hasCommand(commands, "rereadsecrets") || hasCommand(commands, "reload") || hasCommand(commands, "start") {
		t.Fatalf("identical healthy config restarted/reloaded strongSwan: %v", commands)
	}
}

func hasCommand(commands [][]string, args ...string) bool {
	for _, command := range commands {
		if reflect.DeepEqual(command, args) {
			return true
		}
	}
	return false
}

func TestEnsureIKEv2FirewallPreservesExistingRules(t *testing.T) {
	old := ikev2FirewallCommand
	t.Cleanup(func() { ikev2FirewallCommand = old })
	rules := map[string]bool{}
	inputAttached := false
	var calls [][]string
	ikev2FirewallCommand = func(_ context.Context, args ...string) ([]byte, error) {
		call := append([]string(nil), args...)
		calls = append(calls, call)
		if len(args) > 2 && args[2] == "-C" {
			if len(args) > 3 && args[3] == "INPUT" {
				if inputAttached {
					return nil, nil
				}
				return nil, errors.New("rule missing")
			}
			if rules[strings.Join(args[3:], " ")] {
				return nil, nil
			}
			return nil, errors.New("rule missing")
		}
		if len(args) > 2 && (args[2] == "-A" || args[2] == "-I") {
			if args[3] == "INPUT" {
				inputAttached = true
			} else {
				rules[strings.Join(args[3:], " ")] = true
			}
		}
		return nil, nil
	}
	if err := ensureIKEv2Firewall(); err != nil {
		t.Fatal(err)
	}
	firstCount := len(calls)
	if err := ensureIKEv2Firewall(); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls[firstCount:] {
		if len(call) > 2 && (call[2] == "-A" || call[2] == "-I" || call[2] == "-F") {
			t.Fatalf("second ensure mutated existing firewall state: %v", call)
		}
	}
	if len(calls) == 0 || reflect.DeepEqual(calls[:firstCount], calls[firstCount:]) {
		t.Fatal("expected both initial and idempotent firewall checks")
	}
}
