package nodeagent

import (
	"reflect"
	"testing"
)

func TestBuildOpenVPNTProxySpec(t *testing.T) {
	spec, err := buildOpenVPNTProxySpec(openVPNRuntimeInbound{
		Tag:        "openvpn-main",
		TunnelPort: 29986,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.66.0.0/16",
			"tproxy_enabled": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !spec.Enabled {
		t.Fatal("expected tproxy enabled")
	}
	if spec.SourceCIDR != "10.66.0.0/16" {
		t.Fatalf("unexpected source CIDR: %q", spec.SourceCIDR)
	}
	if spec.TunnelPort != 29986 {
		t.Fatalf("unexpected tunnel port: %d", spec.TunnelPort)
	}
	if spec.Interface != openVPNTunName("openvpn-main") {
		t.Fatalf("unexpected interface: %q", spec.Interface)
	}
}

func TestOpenVPNTProxyTCPRule(t *testing.T) {
	spec, err := buildOpenVPNTProxySpec(openVPNRuntimeInbound{
		Tag:        "openvpn-main",
		TunnelPort: 29986,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.66.0.0/16",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := spec.iptablesArgs("-A", "tcp")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"-t", "mangle",
		"-A", openVPNTProxyChain,
		"-i", openVPNTunName("openvpn-main"),
		"-s", "10.66.0.0/16",
		"-p", "tcp",
		"-j", "TPROXY",
		"--on-ip", "127.0.0.1",
		"--on-port", "29986",
		"--tproxy-mark", spec.markMask(),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected rule:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestOpenVPNTProxyUDPRule(t *testing.T) {
	spec, err := buildOpenVPNTProxySpec(openVPNRuntimeInbound{
		Tag:        "openvpn-main",
		TunnelPort: 29986,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.66.0.0/16",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	args, err := spec.iptablesArgs("-A", "udp")
	if err != nil {
		t.Fatal(err)
	}

	foundUDP := false
	for i := range args {
		if args[i] == "udp" {
			foundUDP = true
			break
		}
	}

	if !foundUDP {
		t.Fatalf("UDP protocol missing: %#v", args)
	}
}

func TestOpenVPNTProxyDisabled(t *testing.T) {
	spec, err := buildOpenVPNTProxySpec(openVPNRuntimeInbound{
		Tag: "openvpn-main",
		Settings: map[string]any{
			"tproxy_enabled": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if spec.Enabled {
		t.Fatal("expected tproxy disabled")
	}
}

func TestOpenVPNTProxyRequiresTunnelPort(t *testing.T) {
	_, err := buildOpenVPNTProxySpec(openVPNRuntimeInbound{
		Tag: "openvpn-main",
		Settings: map[string]any{
			"tproxy_enabled": true,
		},
	})

	if err == nil {
		t.Fatal("expected tunnel port error")
	}
}
