package nodeagent

import (
	"strings"
	"testing"
)

func TestRenderAnyConnectConfig(t *testing.T) {
	inbound := anyConnectRuntimeInbound{
		Tag:  "anyconnect-main",
		Port: 443,
		Settings: map[string]any{
			"ipv4_pool_cidr":     "10.71.0.0/24",
			"dns_servers":        []any{"1.1.1.1", "8.8.8.8"},
			"udp_enabled":        true,
			"udp_port":           8443,
			"max_clients":        128,
			"max_same_clients":   0,
			"server_certificate": "certificate-pem",
			"server_key":         "private-key-pem",
		},
		Users: []anyConnectRuntimeUser{{
			UserID: 1, Username: "alice", Password: "secret",
			IPv4Address: "10.71.0.10", DeviceLimit: 2,
		}},
	}
	files := anyConnectRuntimeFiles{
		PasswordFile:  "/run/antimage/ocpasswd",
		ServerCert:    "/run/antimage/server.crt",
		ServerKey:     "/run/antimage/server.key",
		UserConfigDir: "/run/antimage/users",
		ControlSocket: "/run/antimage/ocserv.sock",
		PIDFile:       "/run/antimage/ocserv.pid",
	}

	config, err := renderAnyConnectConfig(inbound, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`auth = "plain[passwd=/run/antimage/ocpasswd]"`,
		"tcp-port = 443",
		"udp-port = 8443",
		"ipv4-network = 10.71.0.0",
		"ipv4-netmask = 255.255.255.0",
		"dns = 1.1.1.1",
		"dns = 8.8.8.8",
		"config-per-user = /run/antimage/users",
		"socket-file = /run/antimage/ocserv.sock",
		"pid-file = /run/antimage/ocserv.pid",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("missing %q in config:\n%s", expected, config)
		}
	}
	if strings.Contains(config, "secret") || strings.Contains(config, "private-key-pem") {
		t.Fatalf("secret leaked into public config: %s", config)
	}

	userConfig := renderAnyConnectUserConfig(inbound.Users[0], inbound.Settings)
	if !strings.Contains(userConfig, "explicit-ipv4 = 10.71.0.10") {
		t.Fatalf("missing fixed address: %s", userConfig)
	}
	if !strings.Contains(userConfig, "max-same-clients = 2") {
		t.Fatalf("device limit not rendered: %s", userConfig)
	}
}

func TestRenderAnyConnectConfigRejectsInvalidPool(t *testing.T) {
	_, err := renderAnyConnectConfig(anyConnectRuntimeInbound{
		Tag: "bad", Port: 443,
		Settings: map[string]any{"ipv4_pool_cidr": "not-a-cidr"},
	}, anyConnectRuntimeFiles{})
	if err == nil {
		t.Fatal("expected invalid pool error")
	}
}
