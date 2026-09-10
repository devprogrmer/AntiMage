package nodeagent

import (
	"strings"
	"testing"
)

func TestRenderOpenVPNServerConfig(t *testing.T) {
	inbound := openVPNRuntimeInbound{
		Tag:       "openvpn-main",
		Port:      1194,
		Transport: "udp",
		Settings: map[string]any{
			"ipv4_pool_cidr":   "10.66.0.0/16",
			"redirect_gateway": true,
			"dns_servers":      []any{"1.1.1.1", "8.8.8.8"},
			"auth":             "SHA256",
		},
	}

	config, err := renderOpenVPNServerConfig(inbound, openVPNRuntimeFiles{
		CAFile:          "/var/lib/antimage-node/openvpn/main/ca.crt",
		CertFile:        "/var/lib/antimage-node/openvpn/main/server.crt",
		KeyFile:         "/var/lib/antimage-node/openvpn/main/server.key",
		AuthScript:      "/var/lib/antimage-node/openvpn/main/auth.sh",
		ClientConfigDir: "/var/lib/antimage-node/openvpn/main/ccd",
		IPPoolPersist:   "/var/lib/antimage-node/openvpn/main/ipp.txt",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"port 1194",
		"proto udp",
		"topology subnet",
		"server 10.66.0.0 255.255.0.0",
		"verify-client-cert none",
		"username-as-common-name",
		"auth-user-pass-verify ",
		`push "redirect-gateway def1"`,
		`push "dhcp-option DNS 1.1.1.1"`,
		`push "dhcp-option DNS 8.8.8.8"`,
		"auth SHA256",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("missing %q in config:\n%s", expected, config)
		}
	}
}

func TestRenderOpenVPNServerConfigTCP(t *testing.T) {
	config, err := renderOpenVPNServerConfig(openVPNRuntimeInbound{
		Tag:       "tcp-main",
		Port:      443,
		Transport: "tcp",
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.77.0.0/24",
		},
	}, openVPNRuntimeFiles{
		CAFile:          "/tmp/ca.crt",
		CertFile:        "/tmp/server.crt",
		KeyFile:         "/tmp/server.key",
		AuthScript:      "/tmp/auth.sh",
		ClientConfigDir: "/tmp/ccd",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(config, "proto tcp-server") {
		t.Fatalf("expected tcp-server:\n%s", config)
	}
}

func TestOpenVPNPoolNetworkRejectsInvalidCIDR(t *testing.T) {
	if _, _, err := openVPNPoolNetwork("not-a-cidr"); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}

func TestOpenVPNTunNameIsStableAndShort(t *testing.T) {
	first := openVPNTunName("openvpn-main")
	second := openVPNTunName("openvpn-main")

	if first != second {
		t.Fatal("tun name must be deterministic")
	}
	if len(first) > 15 {
		t.Fatalf("tun name too long: %q", first)
	}
}
