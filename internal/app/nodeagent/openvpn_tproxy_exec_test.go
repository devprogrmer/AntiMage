package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureOpenVPNTProxy(t *testing.T) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"

	openVPNNetworkLookPath = func(name string) (string, error) {
		return name, nil
	}

	var commands []string

	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)

		if name == "ip" && strings.Join(args, " ") == "rule show" {
			return []byte("0: from all lookup local\n"), nil
		}

		if name == "iptables" && containsArg(args, "-C") {
			return nil, errors.New("rule does not exist")
		}

		return nil, nil
	}

	server := New(Config{DataDir: t.TempDir()})

	spec := openVPNTProxySpec{
		Enabled:    true,
		Interface:  "amov1234",
		SourceCIDR: "10.66.0.0/16",
		TunnelPort: 29986,
		Mark:       openVPNTProxyMark,
		Mask:       openVPNTProxyMask,
		Table:      openVPNTProxyTable,
		Priority:   openVPNTProxyRulePriority,
	}

	if err := server.ensureOpenVPNTProxy(spec); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"ip rule add priority 10010 fwmark 0xa17e0000/0xffff0000 table 201",
		"ip route replace local 0.0.0.0/0 dev lo table 201",
		"-A PREROUTING",
		"-p tcp",
		"-p udp",
		"--on-port 29986",
	} {
		if !commandsContain(commands, expected) {
			t.Fatalf(
				"missing command fragment %q:\n%s",
				expected,
				strings.Join(commands, "\n"),
			)
		}
	}
}

func TestEnsureOpenVPNTProxyIsIdempotent(t *testing.T) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"
	openVPNNetworkLookPath = func(name string) (string, error) {
		return name, nil
	}

	var commands []string

	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)

		if name == "ip" && strings.Join(args, " ") == "rule show" {
			return []byte(
				"10010: from all fwmark 0xa17e0000/0xffff0000 lookup 201\n",
			), nil
		}

		return nil, nil
	}

	server := New(Config{DataDir: t.TempDir()})

	spec := openVPNTProxySpec{
		Enabled:    true,
		Interface:  "amov1234",
		SourceCIDR: "10.66.0.0/16",
		TunnelPort: 29986,
		Mark:       openVPNTProxyMark,
		Mask:       openVPNTProxyMask,
		Table:      openVPNTProxyTable,
		Priority:   openVPNTProxyRulePriority,
	}

	if err := server.ensureOpenVPNTProxy(spec); err != nil {
		t.Fatal(err)
	}

	if commandsContain(commands, "ip rule add") {
		t.Fatalf(
			"existing policy rule was duplicated:\n%s",
			strings.Join(commands, "\n"),
		)
	}

	if commandsContain(commands, "-A PREROUTING") {
		t.Fatalf(
			"existing iptables rule was duplicated:\n%s",
			strings.Join(commands, "\n"),
		)
	}
}

func TestRemoveOpenVPNTProxy(t *testing.T) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"
	openVPNNetworkLookPath = func(name string) (string, error) {
		return name, nil
	}

	var commands []string

	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		commands = append(
			commands,
			name+" "+strings.Join(args, " "),
		)
		return nil, nil
	}

	server := New(Config{DataDir: t.TempDir()})

	spec := openVPNTProxySpec{
		Enabled:    true,
		Interface:  "amov1234",
		SourceCIDR: "10.66.0.0/16",
		TunnelPort: 29986,
		Mark:       openVPNTProxyMark,
		Mask:       openVPNTProxyMask,
		Table:      openVPNTProxyTable,
		Priority:   openVPNTProxyRulePriority,
	}

	if err := server.removeOpenVPNTProxy(spec); err != nil {
		t.Fatal(err)
	}

	if !commandsContain(commands, "-D "+openVPNTProxyChain) {
		t.Fatalf(
			"TPROXY delete command missing:\n%s",
			strings.Join(commands, "\n"),
		)
	}
}

func containsArg(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func commandsContain(commands []string, expected string) bool {
	for _, command := range commands {
		if strings.Contains(command, expected) {
			return true
		}
	}
	return false
}
