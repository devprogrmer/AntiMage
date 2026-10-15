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
	l2TPLookPath = func(string) (string, error) { return os.Args[0], nil }
	l2TPCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		commands = append(commands, append([]string(nil), args...))
		return ikev2SuccessCommand(ctx, args...)
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

func TestIKEv2UserAddUpdateRemoveSyncReloadsSecretsAndKeepsOtherConnections(t *testing.T) {
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
	l2TPLookPath = func(string) (string, error) { return os.Args[0], nil }
	l2TPCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return ikev2SuccessCommand(ctx, args...)
	}
	ikev2FirewallCommand = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() {
		l2TPLookPath, l2TPCommandContext, ikev2FirewallCommand = oldLookPath, oldCommand, oldFirewall
	})

	generated := filepath.Join(root, "generated")
	if err := os.MkdirAll(generated, 0700); err != nil {
		t.Fatal(err)
	}
	makeRuntime := func(tag, connection, secrets string) preparedIKEv2Runtime {
		t.Helper()
		files := ikev2RuntimeFiles{
			IPSecConfig: filepath.Join(generated, tag+".conf"),
			IPSecSecret: filepath.Join(generated, tag+".secrets"),
			CACert:      filepath.Join(generated, tag+".ca"),
			ServerCert:  filepath.Join(generated, tag+".crt"),
			ServerKey:   filepath.Join(generated, tag+".key"),
			CAName:      tag + "-ca.pem", CertName: tag + "-server.pem", KeyName: tag + "-server.key",
		}
		for path, content := range map[string]string{
			files.IPSecConfig: connection, files.IPSecSecret: secrets,
			files.CACert: "ca", files.ServerCert: "cert", files.ServerKey: "key",
		} {
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return preparedIKEv2Runtime{Tag: tag, Inbound: ikev2RuntimeInbound{Tag: tag}, Files: files}
	}
	primary := makeRuntime("ikev2-primary", "conn antimage-primary\n", ": RSA primary.key\n\"alice\" : EAP \"a1\"\n")
	other := makeRuntime("ikev2-other", "conn antimage-other\n", ": RSA other.key\n\"carol\" : EAP \"c1\"\n")
	runtimes := []preparedIKEv2Runtime{primary, other}
	server := New(Config{DataDir: filepath.Join(root, "data")})

	apply := func(t *testing.T, wantPresent, wantAbsent []string) {
		t.Helper()
		if err := server.applyIKEv2Runtimes(runtimes); err != nil {
			t.Fatal(err)
		}
		installedSecrets, err := os.ReadFile(l2TPIPSecSecretsPath)
		if err != nil {
			t.Fatal(err)
		}
		installedConfig, err := os.ReadFile(l2TPIPSecConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range wantPresent {
			if !strings.Contains(string(installedSecrets), value) && !strings.Contains(string(installedConfig), value) {
				t.Fatalf("installed IKEv2 files missing %q", value)
			}
		}
		for _, value := range wantAbsent {
			if value == "" {
				continue
			}
			if strings.Contains(string(installedSecrets), value) {
				t.Fatalf("installed IKEv2 secrets still contain %q: %s", value, installedSecrets)
			}
		}
	}

	apply(t, []string{"alice", "carol", "antimage-primary", "antimage-other"}, nil)
	writeSecrets := func(value string) {
		t.Helper()
		if err := os.WriteFile(primary.Files.IPSecSecret, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutation := range []struct {
		name, secrets, present, absent string
	}{
		{name: "add", secrets: ": RSA primary.key\n\"alice\" : EAP \"a1\"\n\"bob\" : EAP \"b1\"\n", present: "bob", absent: ""},
		{name: "update", secrets: ": RSA primary.key\n\"alice\" : EAP \"a2\"\n\"bob\" : EAP \"b1\"\n", present: `"alice" : EAP "a2"`, absent: `"alice" : EAP "a1"`},
		{name: "remove", secrets: ": RSA primary.key\n\"alice\" : EAP \"a2\"\n", present: "carol", absent: "bob"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			writeSecrets(mutation.secrets)
			commands := [][]string{}
			l2TPCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
				commands = append(commands, append([]string(nil), args...))
				return ikev2SuccessCommand(ctx, args...)
			}
			apply(t, []string{mutation.present, "carol", "antimage-primary", "antimage-other"}, []string{mutation.absent})
			if !hasCommand(commands, "rereadsecrets") || !hasCommand(commands, "reload") {
				t.Fatalf("user-only secrets change did not reload credentials/config: %v", commands)
			}
			if hasCommand(commands, "start") || hasCommand(commands, "stop") {
				t.Fatalf("user-only sync restarted strongSwan: %v", commands)
			}
		})
	}
}

func ikev2SuccessCommand(ctx context.Context, args ...string) *exec.Cmd {
	allArgs := append([]string{"-test.run=TestIKEv2CommandHelper", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], allArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_IKEV2_COMMAND_HELPER=1")
	return cmd
}

func TestIKEv2CommandHelper(t *testing.T) {
	if os.Getenv("GO_WANT_IKEV2_COMMAND_HELPER") == "1" {
		os.Exit(0)
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
