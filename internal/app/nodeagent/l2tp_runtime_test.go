package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNativeRuntimePayloadL2TP(t *testing.T) {
	payload, err := parseNativeRuntimePayload(`{
		"l2tp_inbounds": [{
			"tag": "l2tp-main",
			"tunnel_tag": "__antimage_l2tp_tunnel__l2tp-main",
			"port": 1701,
			"tunnel_port": 1702,
			"settings": {
				"ipsec_psk": "shared",
				"ipv4_pool_cidr": "10.67.0.0/16",
				"tproxy_enabled": true
			},
			"users": [{
				"user_id": 42,
				"username": "alice",
				"vpn_username": "alice-vpn",
				"password": "secret",
				"ipv4_address": "10.67.0.2",
				"status": "active"
			}]
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.L2TPInbounds) != 1 {
		t.Fatalf("l2tp inbounds = %d, want 1", len(payload.L2TPInbounds))
	}
	inbound := payload.L2TPInbounds[0]
	if inbound.Tag != "l2tp-main" || inbound.Port != 1701 || inbound.TunnelPort != 1702 {
		t.Fatalf("unexpected inbound: %#v", inbound)
	}
	if len(inbound.Users) != 1 || inbound.Users[0].VPNUsername != "alice-vpn" {
		t.Fatalf("unexpected users: %#v", inbound.Users)
	}
}

func TestPrepareL2TPInboundRendersDaemonConfigs(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	files, err := server.prepareL2TPInbound(l2TPRuntimeInbound{
		Tag:        "l2tp-main",
		Port:       1701,
		TunnelPort: 1702,
		Settings: map[string]any{
			"ipsec_psk":      "shared secret",
			"ipv4_pool_cidr": "10.67.0.0/16",
			"dns_servers":    []any{"1.1.1.1", "8.8.8.8"},
		},
		Users: []l2TPRuntimeUser{{
			UserID:      42,
			Username:    "alice",
			VPNUsername: "alice-vpn",
			Password:    "secret",
			IPv4Address: "10.67.0.42",
			Status:      "active",
		}},
	}, nativeRuntimeSessionCallback{})
	if err != nil {
		t.Fatal(err)
	}
	rawXL2TP, err := os.ReadFile(files.XL2TPConfig)
	if err != nil {
		t.Fatal(err)
	}
	rawPPP, err := os.ReadFile(files.PPPOptions)
	if err != nil {
		t.Fatal(err)
	}
	rawSecrets, err := os.ReadFile(files.CHAPSecrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ip range = 10.67.0.2-10.67.255.254",
		"local ip = 10.67.0.1",
		"pppoptfile = " + filepath.ToSlash(files.PPPOptions),
	} {
		if !strings.Contains(string(rawXL2TP), expected) {
			t.Fatalf("xl2tp config missing %q:\n%s", expected, rawXL2TP)
		}
	}
	for _, expected := range []string{
		"require-mschap-v2",
		"refuse-eap",
		"refuse-mschap",
		"refuse-chap",
		"refuse-pap",
		"ms-dns 1.1.1.1",
		"ms-dns 8.8.8.8",
		"mtu 1410",
	} {
		if !strings.Contains(string(rawPPP), expected) {
			t.Fatalf("ppp options missing %q:\n%s", expected, rawPPP)
		}
	}
	if !strings.Contains(string(rawSecrets), `"alice-vpn"`) ||
		!strings.Contains(string(rawSecrets), "10.67.0.42") {
		t.Fatalf("chap secrets missing user binding:\n%s", rawSecrets)
	}
}

func TestL2TPNetworkSpecsUseRebeccaCompatibleDefaults(t *testing.T) {
	inbound := l2TPRuntimeInbound{
		Tag:        "l2tp-main",
		TunnelPort: 1702,
		Settings: map[string]any{
			"ipv4_pool_cidr": "10.67.0.0/16",
			"tproxy_enabled": true,
		},
	}
	tproxy, err := buildL2TPTProxySpec(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if tproxy.Interface != "ppp+" || tproxy.SourceCIDR != "10.67.0.0/16" || tproxy.TunnelPort != 1702 {
		t.Fatalf("unexpected tproxy spec: %#v", tproxy)
	}
	if tproxy.Mark != l2TPTProxyMark || tproxy.Table != l2TPTProxyTable {
		t.Fatalf("unexpected mark/table: %#v", tproxy)
	}
	nat, err := buildL2TPNATSpec(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if !nat.Enabled || nat.SourceCIDR != "10.67.0.0/16" {
		t.Fatalf("unexpected nat spec: %#v", nat)
	}
}

func TestApplyNativeRuntimeL2TPPreflightBeforeNetworkMutation(t *testing.T) {
	oldLookPath := l2TPLookPath
	oldRun := openVPNNetworkRun
	defer func() {
		l2TPLookPath = oldLookPath
		openVPNNetworkRun = oldRun
	}()
	l2TPLookPath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}
	var commands []string
	openVPNNetworkRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, errors.New("network should not be touched")
	}
	err := New(Config{DataDir: t.TempDir()}).applyNativeRuntime(`{
		"l2tp_inbounds": [{
			"tag": "l2tp-main",
			"port": 1701,
			"tunnel_port": 1702,
			"settings": {
				"ipsec_psk": "shared",
				"ipv4_pool_cidr": "10.67.0.0/16"
			},
			"users": [{
				"user_id": 42,
				"username": "alice",
				"password": "secret",
				"ipv4_address": "10.67.0.2",
				"status": "active"
			}]
		}]
	}`)
	if err == nil || !strings.Contains(err.Error(), `executable "ipsec" not installed`) {
		t.Fatalf("error = %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("network mutated before preflight: %v", commands)
	}
}

func TestApplyL2TPNATUsesRuntimePoolCIDR(t *testing.T) {
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
		if strings.Contains(command, "-C "+l2TPNATChain) ||
			strings.Contains(command, "-S "+l2TPNATChain) ||
			strings.Contains(command, "-C POSTROUTING") {
			return nil, errOpenVPNTestMissingRule{}
		}
		return nil, nil
	}
	spec, err := buildL2TPNATSpec(l2TPRuntimeInbound{
		Tag:      "l2tp-main",
		Settings: map[string]any{"ipv4_pool_cidr": "10.67.0.0/16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(Config{DataDir: t.TempDir()}).applyL2TPNAT("l2tp-main", spec); err != nil {
		t.Fatal(err)
	}
	if !commandsContain(commands, "iptables -w 5 -t nat -A "+l2TPNATChain+" -s 10.67.0.0/16 -j MASQUERADE") {
		t.Fatalf("missing L2TP MASQUERADE rule:\n%s", strings.Join(commands, "\n"))
	}
}

func TestStopRemovedL2TPRuntimesKeepsSystemServiceForReplacement(t *testing.T) {
	oldLookPath := l2TPLookPath
	defer func() {
		l2TPLookPath = oldLookPath
	}()

	lookPathCalls := 0
	l2TPLookPath = func(name string) (string, error) {
		lookPathCalls++
		return "", exec.ErrNotFound
	}

	server := New(Config{DataDir: t.TempDir()})
	server.l2TPRuntimes["old-l2tp"] = &l2TPProcess{
		tag: "old-l2tp",
	}

	server.stopRemovedL2TPRuntimes(map[string]struct{}{
		"new-l2tp": {},
	})

	if lookPathCalls != 0 {
		t.Fatalf(
			"system L2TP service/config touched during replacement: lookPath calls = %d",
			lookPathCalls,
		)
	}

	if _, ok := server.l2TPRuntimes["old-l2tp"]; ok {
		t.Fatal("old L2TP runtime was not removed from runtime registry")
	}
}
