package nodeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNativeRuntimePayloadOpenVPN(t *testing.T) {
	raw := `{
"generated_at":"2026-09-10T00:00:00Z",
"target":"node:1",
"inbounds":[{
"tag":"openvpn-main",
"tunnel_tag":"__antimage_ov_tunnel__openvpn-main",
"port":1194,
"transport":"udp",
"tunnel_port":29986,
"settings":{
"ipv4_pool_cidr":"10.66.0.0/16",
"tproxy_enabled":true
},
"users":[{
"user_id":42,
"username":"test",
"vpn_username":"test",
"password":"secret",
"ipv4_address":"10.66.0.2",
"status":"active",
"used_traffic":0,
"device_limit":1
}]
}]
}`

	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(payload.OpenVPNInbounds) != 1 {
		t.Fatalf("expected 1 OpenVPN inbound, got %d", len(payload.OpenVPNInbounds))
	}

	inbound := payload.OpenVPNInbounds[0]

	if inbound.Tag != "openvpn-main" {
		t.Fatalf("unexpected tag: %q", inbound.Tag)
	}

	if inbound.Port != 1194 {
		t.Fatalf("unexpected port: %d", inbound.Port)
	}

	if inbound.Transport != "udp" {
		t.Fatalf("unexpected transport: %q", inbound.Transport)
	}

	if inbound.TunnelPort != 29986 {
		t.Fatalf("unexpected tunnel port: %d", inbound.TunnelPort)
	}

	if len(inbound.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(inbound.Users))
	}

	if inbound.Users[0].UserID != 42 {
		t.Fatalf("unexpected user id: %d", inbound.Users[0].UserID)
	}
}

func TestParseNativeRuntimePayloadEmpty(t *testing.T) {
	payload, err := parseNativeRuntimePayload("")
	if err != nil {
		t.Fatal(err)
	}

	if len(payload.OpenVPNInbounds) != 0 {
		t.Fatalf("expected no OpenVPN inbounds")
	}
}

func TestParseNativeRuntimePayloadRejectsInvalidJSON(t *testing.T) {
	if _, err := parseNativeRuntimePayload(`{invalid`); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestParseNativeRuntimeSessionCallback(t *testing.T) {
	payload, err := parseNativeRuntimePayload(`{
"generated_at": "2026-09-10T00:00:00Z",
"target": "node:7",
"session_callback": {
"url": "http://127.0.0.1:8443/internal/node/session-event",
"token": "test-token",
"node_id": 7
},
"inbounds": []
}`)
	if err != nil {
		t.Fatal(err)
	}

	if payload.SessionCallback.URL != "http://127.0.0.1:8443/internal/node/session-event" {
		t.Fatalf(
			"unexpected callback URL: %q",
			payload.SessionCallback.URL,
		)
	}

	if payload.SessionCallback.Token != "test-token" {
		t.Fatalf("unexpected callback token")
	}

	if payload.SessionCallback.NodeID != 7 {
		t.Fatalf(
			"unexpected callback node ID: %d",
			payload.SessionCallback.NodeID,
		)
	}
}

func TestParseNativeRuntimePayloadWireGuard(t *testing.T) {
	payload, err := parseNativeRuntimePayload(`{
"generated_at":"2026-09-11T00:00:00Z",
"target":"node:1",
"wg_inbounds":[{
"tag":"wg-main",
"listen_port":51820,
"tunnel_port":29987,
"settings":{
"accounting_enabled":true,
"interface_name":"wg-test0"
},
"peers":[{
"user_id":42,
"username":"alice",
"public_key":"peer-a",
"address":"10.69.0.2",
"status":"active",
"used_traffic":0,
"device_limit":1
}]
}]
}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.WireGuardInbounds) != 1 {
		t.Fatalf(
			"expected 1 WireGuard inbound, got %d",
			len(payload.WireGuardInbounds),
		)
	}
	inbound := payload.WireGuardInbounds[0]
	if inbound.Tag != "wg-main" || inbound.ListenPort != 51820 {
		t.Fatalf("unexpected inbound: %#v", inbound)
	}
	if len(inbound.Peers) != 1 ||
		inbound.Peers[0].UserID != 42 ||
		inbound.Peers[0].PublicKey != "peer-a" {
		t.Fatalf("unexpected peers: %#v", inbound.Peers)
	}
}

func TestApplyNativeRuntimeValidatesOpenVPNBeforeWireGuardUsageSync(
	t *testing.T,
) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})

	err := server.applyNativeRuntime(`{
		"wg_inbounds": [{
			"tag": "wg-main",
			"listen_port": 51820,
			"settings": {
				"private_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				"interface_name": "wg-test0",
				"accounting_enabled": true,
				"tproxy_enabled": false,
				"nat_enabled": false
			},
			"peers": [{
				"user_id": 42,
				"username": "alice",
				"public_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				"address": "10.69.0.2",
				"status": "active"
			}]
		}],
		"inbounds": [{
			"tag": "",
			"port": 1194,
			"transport": "udp"
		}]
	}`)
	if err == nil ||
		!strings.Contains(err.Error(), "openvpn inbound tag is required") {
		t.Fatalf("error = %v", err)
	}

	usageRoot := filepath.Join(dataDir, "wireguard", "inbounds")
	if _, statErr := os.Stat(usageRoot); !os.IsNotExist(statErr) {
		if statErr != nil {
			t.Fatal(statErr)
		}
		t.Fatalf(
			"wireguard usage state was mutated before OpenVPN validation: %s",
			usageRoot,
		)
	}
}
