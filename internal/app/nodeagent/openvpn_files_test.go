package nodeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareOpenVPNInbound(t *testing.T) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})

	inbound := openVPNRuntimeInbound{
		Tag:       "openvpn-main",
		Port:      1194,
		Transport: "udp",
		Settings: map[string]any{
			"ipv4_pool_cidr":     "10.66.0.0/16",
			"ca":                 "TEST-CA",
			"server_certificate": "TEST-CERT",
			"server_key":         "TEST-KEY",
			"redirect_gateway":   true,
		},
		Users: []openVPNRuntimeUser{
			{
				UserID:      42,
				Username:    "alice",
				VPNUsername: "alice",
				Password:    "secret-password",
				IPv4Address: "10.66.0.2",
				Status:      "active",
			},
		},
	}

	configPath, err := server.prepareOpenVPNInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}

	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(config), "port 1194") {
		t.Fatalf("missing OpenVPN port:\n%s", config)
	}

	root := filepath.Dir(configPath)

	for _, name := range []string{
		"ca.crt",
		"server.crt",
		"server.key",
		"auth.sh",
		"credentials.sha256",
		"ipp.txt",
	} {
		if name == "ipp.txt" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	ccd, err := os.ReadFile(filepath.Join(root, "ccd", "alice"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(ccd), "ifconfig-push 10.66.0.2 255.255.0.0") {
		t.Fatalf("unexpected CCD:\n%s", ccd)
	}

	hashes, err := os.ReadFile(filepath.Join(root, "credentials.sha256"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(hashes), "secret-password") {
		t.Fatal("plaintext password must not be stored in credentials file")
	}
}

func TestPrepareOpenVPNInboundRequiresCertificates(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	_, err := server.prepareOpenVPNInbound(openVPNRuntimeInbound{
		Tag:  "missing-cert",
		Port: 1194,
	})

	if err == nil {
		t.Fatal("expected missing certificate error")
	}
}

func TestValidateOpenVPNUsernameRejectsTraversal(t *testing.T) {
	for _, username := range []string{
		"../root",
		`..\root`,
		"a/b",
		"a" + "\n" + "b",
	} {
		if err := validateOpenVPNUsername(username); err == nil {
			t.Fatalf("expected invalid username %q", username)
		}
	}
}

func TestPrepareOpenVPNInboundWritesAuthPolicy(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	expiredAt := int64(100)
	futureExpire := int64(4102444800)
	limit := int64(1000)

	configPath, err := server.prepareOpenVPNInbound(openVPNRuntimeInbound{
		Tag:       "auth-policy",
		Port:      1194,
		Transport: "udp",
		Settings: map[string]any{
			"ipv4_pool_cidr":     "10.88.0.0/24",
			"ca":                 "TEST-CA",
			"server_certificate": "TEST-CERT",
			"server_key":         "TEST-KEY",
		},
		Users: []openVPNRuntimeUser{
			{
				UserID:      1,
				Username:    "active",
				VPNUsername: "active",
				Password:    "active-password",
				Status:      "active",
				UsedTraffic: 100,
				DataLimit:   &limit,
				Expire:      &futureExpire,
			},
			{
				UserID:      2,
				Username:    "onhold",
				VPNUsername: "onhold",
				Password:    "onhold-password",
				Status:      "on_hold",
			},
			{
				UserID:      3,
				Username:    "limited",
				VPNUsername: "limited",
				Password:    "limited-password",
				Status:      "active",
				UsedTraffic: 1000,
				DataLimit:   &limit,
			},
			{
				UserID:      4,
				Username:    "expired",
				VPNUsername: "expired",
				Password:    "expired-password",
				Status:      "active",
				Expire:      &expiredAt,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(configPath)

	records, err := os.ReadFile(filepath.Join(root, "credentials.sha256"))
	if err != nil {
		t.Fatal(err)
	}

	recordText := string(records)

	for _, expected := range []string{
		"\tactive\t100\t1000\t4102444800",
		"\ton_hold\t0\t0\t0",
		"\tactive\t1000\t1000\t0",
		"\tactive\t0\t0\t100",
	} {
		if !strings.Contains(recordText, expected) {
			t.Fatalf(
				"missing auth record %q:\n%s",
				expected,
				recordText,
			)
		}
	}

	for _, password := range []string{
		"active-password",
		"onhold-password",
		"limited-password",
		"expired-password",
	} {
		if strings.Contains(recordText, password) {
			t.Fatalf("plaintext password leaked: %q", password)
		}
	}

	script, err := os.ReadFile(filepath.Join(root, "auth.sh"))
	if err != nil {
		t.Fatal(err)
	}

	scriptText := string(script)

	for _, expected := range []string{
		`status == "on_hold"`,
		`status == "active"`,
		`limit > 0 && used >= limit`,
		`expire > 0 && expire <= now`,
	} {
		if !strings.Contains(scriptText, expected) {
			t.Fatalf(
				"missing auth policy %q:\n%s",
				expected,
				scriptText,
			)
		}
	}
}

func TestPrepareOpenVPNInboundWritesSessionHooks(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	configPath, err := server.prepareOpenVPNInbound(
		openVPNRuntimeInbound{
			Tag:       "session-hooks",
			Port:      1194,
			Transport: "udp",
			Settings: map[string]any{
				"ipv4_pool_cidr":     "10.88.0.0/24",
				"ca":                 "TEST-CA",
				"server_certificate": "TEST-CERT",
				"server_key":         "TEST-KEY",
			},
			Users: []openVPNRuntimeUser{
				{
					UserID:      42,
					Username:    "alice",
					VPNUsername: "alice",
					Password:    "alice-password",
					IPv4Address: "10.88.0.10",
					Status:      "active",
				},
			},
		},
		nativeRuntimeSessionCallback{
			URL:    "https://controller.example/internal/node/session-event",
			Token:  "callback-super-secret",
			NodeID: 7,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(configPath)

	helperPath := filepath.Join(root, "session-helper.json")
	connectPath := filepath.Join(root, "client-connect.sh")
	disconnectPath := filepath.Join(root, "client-disconnect.sh")

	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := string(configRaw)

	for _, expected := range []string{
		"client-connect " + quoteOpenVPNPath(connectPath),
		"client-disconnect " + quoteOpenVPNPath(disconnectPath),
	} {
		if !strings.Contains(configText, expected) {
			t.Fatalf(
				"missing server config directive %q:\n%s",
				expected,
				configText,
			)
		}
	}

	if strings.Contains(configText, "callback-super-secret") {
		t.Fatal("session callback token leaked into server.conf")
	}

	helperRaw, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	helperText := string(helperRaw)

	for _, expected := range []string{
		`"node_id":7`,
		`"inbound_tag":"session-hooks"`,
		`"alice":42`,
		`"token":"callback-super-secret"`,
	} {
		if !strings.Contains(helperText, expected) {
			t.Fatalf(
				"missing session helper data %q:\n%s",
				expected,
				helperText,
			)
		}
	}

	connectRaw, err := os.ReadFile(connectPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(connectRaw), " session-event ") ||
		!strings.Contains(string(connectRaw), " start\n") {
		t.Fatalf(
			"invalid client-connect wrapper:\n%s",
			connectRaw,
		)
	}

	disconnectRaw, err := os.ReadFile(disconnectPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(disconnectRaw), " session-event ") ||
		!strings.Contains(string(disconnectRaw), " stop\n") {
		t.Fatalf(
			"invalid client-disconnect wrapper:\n%s",
			disconnectRaw,
		)
	}

	// Windows does not preserve Unix permission bits the same way.
	if os.PathSeparator == '/' {
		info, err := os.Stat(helperPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf(
				"unexpected helper permissions: %o",
				got,
			)
		}

		for _, path := range []string{
			connectPath,
			disconnectPath,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0700 {
				t.Fatalf(
					"unexpected wrapper permissions for %s: %o",
					path,
					got,
				)
			}
		}
	}
}

func TestPrepareOpenVPNInboundWritesAccountingStatus(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	configPath, err := server.prepareOpenVPNInbound(
		openVPNRuntimeInbound{
			Tag:       "accounting-status",
			Port:      1194,
			Transport: "udp",
			Settings: map[string]any{
				"ipv4_pool_cidr":     "10.89.0.0/24",
				"ca":                 "TEST-CA",
				"server_certificate": "TEST-CERT",
				"server_key":         "TEST-KEY",
				"accounting_enabled": true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	config := string(raw)
	statusPath := filepath.Join(
		filepath.Dir(configPath),
		"status.tsv",
	)

	expectedStatus := "status " +
		quoteOpenVPNPath(statusPath) +
		" 5"

	if !strings.Contains(config, expectedStatus) {
		t.Fatalf(
			"missing OpenVPN status directive %q:\n%s",
			expectedStatus,
			config,
		)
	}

	if !strings.Contains(config, "status-version 3") {
		t.Fatalf(
			"missing status-version 3:\n%s",
			config,
		)
	}
}

func TestPrepareOpenVPNInboundDisablesAccountingStatus(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	configPath, err := server.prepareOpenVPNInbound(
		openVPNRuntimeInbound{
			Tag:       "accounting-disabled",
			Port:      1194,
			Transport: "udp",
			Settings: map[string]any{
				"ipv4_pool_cidr":     "10.90.0.0/24",
				"ca":                 "TEST-CA",
				"server_certificate": "TEST-CERT",
				"server_key":         "TEST-KEY",
				"accounting_enabled": false,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	config := string(raw)

	if strings.Contains(config, "status-version") {
		t.Fatalf(
			"accounting disabled but status-version exists:\n%s",
			config,
		)
	}

	if strings.Contains(config, "status ") {
		t.Fatalf(
			"accounting disabled but status directive exists:\n%s",
			config,
		)
	}
}
