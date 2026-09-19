package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseNativeRuntimePayloadPPTP(t *testing.T) {
	payload, err := parseNativeRuntimePayload(`{
		"pptp_inbounds": [{
			"tag": "pptp-main",
			"tunnel_tag": "__antimage_pptp_tunnel__pptp-main",
			"port": 1723,
			"tunnel_port": 41942,
			"settings": {
				"ipv4_pool_cidr": "10.68.0.0/24",
				"tproxy_enabled": true
			},
			"users": [{
				"user_id": 42,
				"username": "alice",
				"vpn_username": "alice-vpn",
				"password": "secret",
				"ipv4_address": "10.68.0.42",
				"status": "active"
			}]
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.PPTPInbounds) != 1 {
		t.Fatalf("pptp inbounds = %d, want 1", len(payload.PPTPInbounds))
	}
	inbound := payload.PPTPInbounds[0]
	if inbound.Tag != "pptp-main" || inbound.Port != 1723 || inbound.TunnelPort != 41942 {
		t.Fatalf("unexpected inbound: %#v", inbound)
	}
	if len(inbound.Users) != 1 || inbound.Users[0].VPNUsername != "alice-vpn" {
		t.Fatalf("unexpected users: %#v", inbound.Users)
	}
}

func TestPreparePPTPInboundRendersDaemonConfigs(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	configPath, err := server.preparePPTPInbound(pptpRuntimeInbound{
		Tag:        "pptp-main",
		Port:       1723,
		TunnelPort: 41942,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.68.0.0/24",
			"dns_servers":    []any{"1.1.1.1", "8.8.8.8"},
		},
		Users: []pptpRuntimeUser{{
			UserID:      42,
			Username:    "alice",
			VPNUsername: "alice-vpn",
			Password:    "secret",
			IPv4Address: "10.68.0.42",
			Status:      "active",
		}},
	}, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(configPath)
	rawPPTPD, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	rawPPP, err := os.ReadFile(filepath.Join(root, "ppp-options"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"option " + filepath.ToSlash(filepath.Join(root, "ppp-options")),
		"localip 10.68.0.1",
		"remoteip 10.68.0.2-10.68.0.254",
	} {
		if !strings.Contains(string(rawPPTPD), expected) {
			t.Fatalf("pptpd config missing %q:\n%s", expected, rawPPTPD)
		}
	}
	for _, expected := range []string{
		"require-mschap-v2",
		"require-mppe-128",
		"ms-dns 1.1.1.1",
		"ms-dns 8.8.8.8",
	} {
		if !strings.Contains(string(rawPPP), expected) {
			t.Fatalf("ppp options missing %q:\n%s", expected, rawPPP)
		}
	}
	if strings.Contains(string(rawPPP), "chap-secrets") {
		t.Fatalf("ppp options must not render unsupported chap-secrets option:\n%s", rawPPP)
	}
}

func TestInstallPPTPSystemCHAPSecretsAddsUpdatesDeletesAndDeduplicatesOwnedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chap-secrets")
	oldPath := pptpCHAPSecretsPath
	defer func() { pptpCHAPSecretsPath = oldPath }()
	pptpCHAPSecretsPath = path

	if err := os.WriteFile(path, []byte(`"l2tp-user"	*	"keep"	*
`), 0644); err != nil {
		t.Fatal(err)
	}

	first := renderPPTPCHAPSecrets([]pptpRuntimeUser{
		{
			UserID:      1,
			Username:    "alice",
			Password:    "old-secret",
			IPv4Address: "10.68.0.2",
			Status:      "active",
		},
		{
			UserID:      1,
			Username:    "alice",
			Password:    "old-secret",
			IPv4Address: "10.68.0.2",
			Status:      "active",
		},
		{
			UserID:      2,
			Username:    "bob",
			Password:    "bob-secret",
			IPv4Address: "10.68.0.3",
			Status:      "active",
		},
	})
	if err := installPPTPSystemCHAPSecrets(first); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if count := strings.Count(text, `"alice"`); count != 1 {
		t.Fatalf("alice entries = %d, want 1:\n%s", count, text)
	}
	if !strings.Contains(text, pptpCHAPBlockStart) || !strings.Contains(text, pptpCHAPBlockEnd) {
		t.Fatalf("missing PPTP ownership markers:\n%s", text)
	}
	if !strings.Contains(text, `"l2tp-user"`) {
		t.Fatalf("non-PPTP entry was not preserved:\n%s", text)
	}

	updated := renderPPTPCHAPSecrets([]pptpRuntimeUser{{
		UserID:      1,
		Username:    "alice",
		Password:    "new-secret",
		IPv4Address: "10.68.0.2",
		Status:      "active",
	}})
	if err := installPPTPSystemCHAPSecrets(updated); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, `"bob"`) || strings.Contains(text, "old-secret") {
		t.Fatalf("stale PPTP entry was retained:\n%s", text)
	}
	if !strings.Contains(text, "new-secret") || !strings.Contains(text, `"l2tp-user"`) {
		t.Fatalf("updated PPTP entry or foreign entry missing:\n%s", text)
	}

	if err := clearPPTPSystemCHAPSecrets(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, `"alice"`) || !strings.Contains(text, `"l2tp-user"`) {
		t.Fatalf("PPTP cleanup should remove only owned entries:\n%s", text)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Fatalf("chap-secrets mode = %v, want 0600", mode)
		}
	}
}

func TestInstallPPTPSystemCHAPSecretsMergesMultipleInboundUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chap-secrets")
	oldPath := pptpCHAPSecretsPath
	defer func() { pptpCHAPSecretsPath = oldPath }()
	pptpCHAPSecretsPath = path

	body := renderPPTPSystemCHAPSecrets([]preparedPPTPRuntime{
		{CHAPSecrets: renderPPTPCHAPSecrets([]pptpRuntimeUser{{
			UserID:      1,
			Username:    "alice",
			Password:    "alice-secret",
			IPv4Address: "10.68.0.2",
			Status:      "active",
		}})},
		{CHAPSecrets: renderPPTPCHAPSecrets([]pptpRuntimeUser{{
			UserID:      2,
			Username:    "bob",
			Password:    "bob-secret",
			IPv4Address: "10.69.0.2",
			Status:      "active",
		}})},
	})
	if err := installPPTPSystemCHAPSecrets(body); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, username := range []string{`"alice"`, `"bob"`} {
		if count := strings.Count(text, username); count != 1 {
			t.Fatalf("%s entries = %d, want 1:\n%s", username, count, text)
		}
	}
}

func TestApplyNativeRuntimePPTPInstallsSystemCHAPSecrets(t *testing.T) {
	oldCHAPPath := pptpCHAPSecretsPath
	oldPPTPLookPath := pptpLookPath
	oldPPTPCommand := pptpCommandContext
	oldPPTPGrace := pptpStartupGrace
	oldNetworkRun := openVPNNetworkRun
	oldNetworkLookPath := openVPNNetworkLookPath
	defer func() {
		pptpCHAPSecretsPath = oldCHAPPath
		pptpLookPath = oldPPTPLookPath
		pptpCommandContext = oldPPTPCommand
		pptpStartupGrace = oldPPTPGrace
		openVPNNetworkRun = oldNetworkRun
		openVPNNetworkLookPath = oldNetworkLookPath
	}()

	pptpCHAPSecretsPath = filepath.Join(t.TempDir(), "chap-secrets")
	if err := os.WriteFile(pptpCHAPSecretsPath, []byte(`"foreign"	*	"keep"	*
`), 0600); err != nil {
		t.Fatal(err)
	}
	pptpStartupGrace = 25 * time.Millisecond
	pptpLookPath = func(string) (string, error) { return os.Args[0], nil }
	pptpCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, name, "-test.run=TestPPTPHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_WANT_PPTP_HELPER=1")
		return cmd
	}
	openVPNNetworkLookPath = func(name string) (string, error) { return name, nil }
	openVPNNetworkRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, nil
	}

	server := New(Config{DataDir: t.TempDir()})
	if err := server.applyNativeRuntime(`{
		"pptp_inbounds": [{
			"tag": "pptp-main",
			"port": 1723,
			"tunnel_port": 41942,
			"settings": {
				"ipv4_pool_cidr": "10.68.0.0/24",
				"tproxy_enabled": false
			},
			"users": [{
				"user_id": 42,
				"username": "alice",
				"password": "secret",
				"ipv4_address": "10.68.0.2",
				"status": "active"
			}]
		}]
	}`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.stopAllPPTPRuntimes)

	raw, err := os.ReadFile(pptpCHAPSecretsPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"alice"`) || !strings.Contains(text, `"foreign"`) {
		t.Fatalf("runtime apply did not install PPTP chap secrets while preserving foreign entries:\n%s", text)
	}

	if err := server.applyNativeRuntime(`{}`); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(pptpCHAPSecretsPath)
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, `"alice"`) || !strings.Contains(text, `"foreign"`) {
		t.Fatalf("runtime apply did not clear stale PPTP chap secrets only:\n%s", text)
	}
}

func TestPPTPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PPTP_HELPER") != "1" {
		return
	}
	if runtime.GOOS == "windows" {
		for {
			time.Sleep(time.Second)
		}
	}
	select {}
}

func TestPPTPNetworkSpecsUseRebeccaCompatibleDefaults(t *testing.T) {
	inbound := pptpRuntimeInbound{
		Tag:        "pptp-main",
		TunnelPort: 41942,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.68.0.0/24",
			"tproxy_enabled": true,
		},
	}
	tproxy, err := buildPPTPTProxySpec(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if tproxy.Interface != "ppp+" || tproxy.SourceCIDR != "10.68.0.0/24" || tproxy.TunnelPort != 41942 {
		t.Fatalf("unexpected tproxy spec: %#v", tproxy)
	}
	if tproxy.Mark != pptpTProxyMark || tproxy.Table != pptpTProxyTable {
		t.Fatalf("unexpected mark/table: %#v", tproxy)
	}
	nat, err := buildPPTPNATSpec(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if !nat.Enabled || nat.SourceCIDR != "10.68.0.0/24" {
		t.Fatalf("unexpected nat spec: %#v", nat)
	}
}

func TestPPTPRejectsPoolLargerThan24(t *testing.T) {
	_, err := pptpPoolPrefix(pptpRuntimeInbound{
		Tag:      "pptp-main",
		Settings: map[string]any{"ipv4_pool_cidr": "10.68.0.0/16"},
	})
	if err == nil || !strings.Contains(err.Error(), "IPv4 pool must be /24 or narrower") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyNativeRuntimePPTPPreflightBeforeNetworkMutation(t *testing.T) {
	oldLookPath := pptpLookPath
	oldRun := openVPNNetworkRun
	defer func() {
		pptpLookPath = oldLookPath
		openVPNNetworkRun = oldRun
	}()
	pptpLookPath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}
	var commands []string
	openVPNNetworkRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, errors.New("network should not be touched")
	}
	err := New(Config{DataDir: t.TempDir()}).applyNativeRuntime(`{
		"pptp_inbounds": [{
			"tag": "pptp-main",
			"port": 1723,
			"tunnel_port": 41942,
			"settings": {
				"ipv4_pool_cidr": "10.68.0.0/24"
			},
			"users": [{
				"user_id": 42,
				"username": "alice",
				"password": "secret",
				"ipv4_address": "10.68.0.2",
				"status": "active"
			}]
		}]
	}`)
	if err == nil || !strings.Contains(err.Error(), `executable "pptpd" not installed`) {
		t.Fatalf("error = %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("network mutated before preflight: %v", commands)
	}
}

func TestApplyPPTPNATUsesRuntimePoolCIDR(t *testing.T) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNATGOOS
	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNATGOOS = oldGOOS
	}()
	openVPNNATGOOS = "linux"
	openVPNNetworkLookPath = func(name string) (string, error) { return name, nil }
	var commands []string
	openVPNNetworkRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, "-C "+pptpNATChain) ||
			strings.Contains(command, "-S "+pptpNATChain) ||
			strings.Contains(command, "-C POSTROUTING") {
			return nil, errOpenVPNTestMissingRule{}
		}
		return nil, nil
	}
	spec, err := buildPPTPNATSpec(pptpRuntimeInbound{
		Tag:      "pptp-main",
		Settings: map[string]any{"ipv4_pool_cidr": "10.68.0.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(Config{DataDir: t.TempDir()}).applyPPTPNAT("pptp-main", spec); err != nil {
		t.Fatal(err)
	}
	if !commandsContain(commands, "iptables -w 5 -t nat -A "+pptpNATChain+" -s 10.68.0.0/24 -j MASQUERADE") {
		t.Fatalf("missing PPTP MASQUERADE rule:\n%s", strings.Join(commands, "\n"))
	}
}
