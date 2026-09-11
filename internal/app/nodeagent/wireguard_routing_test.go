package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWireGuardRoutingDoesNotCollideWithOpenVPN(t *testing.T) {
	if wireGuardTProxyMark == openVPNTProxyMark {
		t.Fatal("WireGuard and OpenVPN marks collide")
	}
	if wireGuardTProxyTable == openVPNTProxyTable {
		t.Fatal("WireGuard and OpenVPN policy tables collide")
	}
	if wireGuardTProxyRulePriority == openVPNTProxyRulePriority {
		t.Fatal("WireGuard and OpenVPN rule priorities collide")
	}
}

func TestBuildWireGuardRoutingSpecDefaultsToTProxy(t *testing.T) {
	spec, err := buildWireGuardRoutingSpec(
		wireGuardRuntimeInbound{
			Tag:        "wg-main",
			TunnelPort: 41940,
			Settings:   map[string]any{},
		},
		"amwg1234",
		"10.69.0.0/16",
	)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Mode != wireGuardRoutingTProxy {
		t.Fatalf("mode = %q", spec.Mode)
	}
	if spec.TunnelPort != 41940 {
		t.Fatalf("tunnel port = %d", spec.TunnelPort)
	}
}

func TestBuildWireGuardRoutingSpecUsesNATWhenTProxyDisabled(t *testing.T) {
	spec, err := buildWireGuardRoutingSpec(
		wireGuardRuntimeInbound{
			Tag: "wg-main",
			Settings: map[string]any{
				"tproxy_enabled": false,
				"nat_enabled":    true,
			},
		},
		"amwg1234",
		"10.69.0.0/16",
	)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Mode != wireGuardRoutingNAT {
		t.Fatalf("mode = %q", spec.Mode)
	}
}

func TestBuildWireGuardRoutingSpecRejectsMissingTunnelPort(t *testing.T) {
	_, err := buildWireGuardRoutingSpec(
		wireGuardRuntimeInbound{
			Tag:      "wg-main",
			Settings: map[string]any{},
		},
		"amwg1234",
		"10.69.0.0/16",
	)
	if err == nil || !strings.Contains(err.Error(), "invalid tunnel port") {
		t.Fatalf("error = %v", err)
	}
}

func TestReconcileWireGuardRoutingProgramsTProxyAndNAT(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	oldGOOS := wireGuardRoutingGOOS
	oldLookPath := wireGuardRoutingLookPath
	oldRun := wireGuardRoutingRun
	wireGuardRoutingGOOS = "linux"
	wireGuardRoutingLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	wireGuardRoutingRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)

		if name == "iptables" && strings.Contains(call, " -C ") {
			return nil, errors.New("missing")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRoutingGOOS = oldGOOS
		wireGuardRoutingLookPath = oldLookPath
		wireGuardRoutingRun = oldRun
	})

	prepared := []preparedWireGuardRuntime{
		{
			Tag: "wg-tproxy",
			Routing: wireGuardRoutingSpec{
				Mode:       wireGuardRoutingTProxy,
				Interface:  "amwg1111",
				SourceCIDR: "10.69.0.0/16",
				TunnelPort: 41940,
				Mark:       wireGuardTProxyMark,
				Mask:       wireGuardTProxyMask,
				Table:      wireGuardTProxyTable,
				Priority:   wireGuardTProxyRulePriority,
			},
		},
		{
			Tag: "wg-nat",
			Routing: wireGuardRoutingSpec{
				Mode:       wireGuardRoutingNAT,
				Interface:  "amwg2222",
				SourceCIDR: "10.80.0.0/24",
				Mark:       wireGuardTProxyMark,
				Mask:       wireGuardTProxyMask,
				Table:      wireGuardTProxyTable,
				Priority:   wireGuardTProxyRulePriority,
			},
		},
	}

	if err := server.reconcileWireGuardRouting(prepared); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"ip rule add priority 10020",
		"ip route replace local 0.0.0.0/0 dev lo table 202",
		"-i amwg1111 -s 10.69.0.0/16 -p tcp -j TPROXY --on-ip 127.0.0.1 --on-port 41940",
		"-i amwg1111 -s 10.69.0.0/16 -p udp -j TPROXY --on-ip 127.0.0.1 --on-port 41940",
		"-s 10.80.0.0/24 -j MASQUERADE",
		"-i amwg2222 -s 10.80.0.0/24 -j ACCEPT",
		"-o amwg2222 -d 10.80.0.0/24 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing routing command %q:\n%s", want, joined)
		}
	}
}
