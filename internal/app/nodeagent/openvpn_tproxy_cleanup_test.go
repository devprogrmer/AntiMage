package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCleanupOpenVPNTProxyPolicyIfUnused(
	t *testing.T,
) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"

	openVPNNetworkLookPath = func(
		name string,
	) (string, error) {
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

	server := New(
		Config{
			DataDir: t.TempDir(),
		},
	)

	if err := server.cleanupOpenVPNTProxyPolicyIfUnused(); err != nil {
		t.Fatal(err)
	}

	expected := []string{
		"iptables -w 5 -t mangle -C PREROUTING -j " +
			openVPNTProxyChain,
		"iptables -w 5 -t mangle -D PREROUTING -j " +
			openVPNTProxyChain,
		"iptables -w 5 -t mangle -S " +
			openVPNTProxyChain,
		"iptables -w 5 -t mangle -F " +
			openVPNTProxyChain,
		"iptables -w 5 -t mangle -X " +
			openVPNTProxyChain,
		"ip route del local 0.0.0.0/0 dev lo table 201",
		"ip rule del priority 10010 fwmark " +
			"0xa17e0000/0xffff0000 table 201",
	}

	for _, want := range expected {
		if !commandsContain(commands, want) {
			t.Fatalf(
				"missing command %q:\n%s",
				want,
				strings.Join(commands, "\n"),
			)
		}
	}
}

func TestCleanupOpenVPNTProxyPolicyKeepsActiveRuntime(
	t *testing.T,
) {
	oldRun := openVPNNetworkRun
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"

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

	server := New(Config{
		DataDir: t.TempDir(),
	})

	server.openVPNTProxySpecs["openvpn-main"] =
		openVPNTProxySpec{
			Enabled: true,
		}

	if err := server.cleanupOpenVPNTProxyPolicyIfUnused(); err != nil {
		t.Fatal(err)
	}

	if len(commands) != 0 {
		t.Fatalf(
			"cleanup ran while tproxy was active:\n%s",
			strings.Join(commands, "\n"),
		)
	}
}

func TestCleanupOpenVPNTProxyPolicyTreatsMissingRouteAndRuleAsClean(
	t *testing.T,
) {
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
	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if strings.Contains(command, "ip route del") {
			return []byte("Error: ipv4: FIB table does not exist."), errors.New("exit status 2")
		}
		if strings.Contains(command, "ip rule del") {
			return []byte("RTNETLINK answers: No such file or directory"), errors.New("exit status 2")
		}
		return nil, nil
	}
	server := New(Config{DataDir: t.TempDir()})
	if err := server.cleanupOpenVPNTProxyPolicyIfUnused(); err != nil {
		t.Fatalf("missing route/rule should be already clean: %v", err)
	}
}

func TestCleanupOpenVPNTProxyPolicyReturnsRealDeleteError(
	t *testing.T,
) {
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
	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		if strings.Contains(command, "ip route del") {
			return []byte("permission denied"), errors.New("exit status 2")
		}
		return nil, nil
	}
	server := New(Config{DataDir: t.TempDir()})
	err := server.cleanupOpenVPNTProxyPolicyIfUnused()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected real cleanup error, got %v", err)
	}
}
