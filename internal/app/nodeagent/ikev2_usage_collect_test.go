package nodeagent

import "testing"

func TestParseIKEv2SwanctlRaw(t *testing.T) {
	raw := `list-sa event {antimage-ikev2-deadbeef {uniqueid=42 version=2 state=ESTABLISHED local-host=203.0.113.10 local-port=4500 local-id=vpn.example.com remote-host=198.51.100.20 remote-port=4500 remote-id=alice remote-eap-id=alice initiator=yes initiator-spi=11223344 responder-spi=aabbccdd remote-vips=[10.70.0.2] child-sas {antimage-ikev2-deadbeef-1 {name=antimage-ikev2-deadbeef uniqueid=7 reqid=1 state=INSTALLED mode=TUNNEL protocol=ESP bytes-in=1200 packets-in=10 bytes-out=3400 packets-out=20 local-ts=[0.0.0.0/0] remote-ts=[10.70.0.2/32]}}}}`

	sas := parseIKEv2SwanctlRaw(raw)

	if len(sas) != 1 {
		t.Fatalf("len(sas)=%d, want 1", len(sas))
	}

	sa := sas[0]

	if sa.ConnectionName != "antimage-ikev2-deadbeef" {
		t.Fatalf(
			"connection=%q",
			sa.ConnectionName,
		)
	}

	if sa.RemoteEAPID != "alice" {
		t.Fatalf(
			"remote eap id=%q",
			sa.RemoteEAPID,
		)
	}

	if len(sa.RemoteVIPs) != 1 ||
		sa.RemoteVIPs[0] != "10.70.0.2" {
		t.Fatalf(
			"remote vips=%v",
			sa.RemoteVIPs,
		)
	}

	if len(sa.Children) != 1 {
		t.Fatalf(
			"children=%d",
			len(sa.Children),
		)
	}

	if sa.Children[0].BytesIn != 1200 ||
		sa.Children[0].BytesOut != 3400 {
		t.Fatalf(
			"bytes=%d/%d",
			sa.Children[0].BytesIn,
			sa.Children[0].BytesOut,
		)
	}
}

func TestIKEv2UsageDelta(t *testing.T) {
	if got := ikev2UsageDelta(
		150,
		100,
		true,
	); got != 50 {
		t.Fatalf("delta=%d, want 50", got)
	}

	if got := ikev2UsageDelta(
		20,
		100,
		true,
	); got != 20 {
		t.Fatalf(
			"reset delta=%d, want 20",
			got,
		)
	}

	if got := ikev2UsageDelta(
		77,
		0,
		false,
	); got != 77 {
		t.Fatalf(
			"first delta=%d, want 77",
			got,
		)
	}
}
