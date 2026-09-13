package nodeagent

import (
	"context"
	"strings"
	"testing"
)

func TestApplyOpenVPNNATUsesRuntimePoolCIDR(t *testing.T) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNATGOOS
	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNATGOOS = oldGOOS
	}()
	openVPNNATGOOS = "linux"
	openVPNNetworkLookPath = func(name string) (string, error) {
		return name, nil
	}
	var commands []string
	openVPNNetworkRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, "-C "+openVPNNATChain) ||
			strings.Contains(command, "-S "+openVPNNATChain) ||
			strings.Contains(command, "-C POSTROUTING") {
			return nil, errOpenVPNTestMissingRule{}
		}
		return nil, nil
	}
	spec, err := buildOpenVPNNATSpec(openVPNRuntimeInbound{
		Tag:      "openvpn-main",
		Settings: map[string]any{"ipv4_pool_cidr": "10.66.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(Config{DataDir: t.TempDir()}).applyOpenVPNNAT("openvpn-main", spec); err != nil {
		t.Fatal(err)
	}
	if !commandsContain(commands, "iptables -w 5 -t nat -A "+openVPNNATChain+" -s 10.66.0.0/16 -j MASQUERADE") {
		t.Fatalf("missing OpenVPN MASQUERADE rule:\n%s", strings.Join(commands, "\n"))
	}
}

type errOpenVPNTestMissingRule struct{}

func (errOpenVPNTestMissingRule) Error() string { return "missing rule" }
