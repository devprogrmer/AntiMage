package nodeagent

import (
	"context"
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

	server.cleanupOpenVPNTProxyPolicyIfUnused()

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

	server.cleanupOpenVPNTProxyPolicyIfUnused()

	if len(commands) != 0 {
		t.Fatalf(
			"cleanup ran while tproxy was active:\n%s",
			strings.Join(commands, "\n"),
		)
	}
}
