package nodeagent

import "testing"

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
