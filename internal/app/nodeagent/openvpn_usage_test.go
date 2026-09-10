package nodeagent

import "testing"

func TestParseOpenVPNStatusV3(t *testing.T) {
	raw := "" +
		"TITLE\tOpenVPN 2.6\n" +
		"TIME\t2026-09-10 12:00:00\t1789041600\n" +
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\tData Channel Cipher\n" +
		"CLIENT_LIST\talice\t203.0.113.10:54321\t10.66.0.10\t\t1200\t3400\t2026-09-10 11:55:00\t1789041300\talice\t7\t0\tAES-256-GCM\n" +
		"END\n"

	clients, err := parseOpenVPNStatusV3(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}

	client := clients[0]

	if client.Username != "alice" {
		t.Fatalf("unexpected username: %q", client.Username)
	}

	if client.ClientIP != "203.0.113.10" {
		t.Fatalf("unexpected client ip: %q", client.ClientIP)
	}

	if client.VirtualAddress != "10.66.0.10" {
		t.Fatalf(
			"unexpected virtual address: %q",
			client.VirtualAddress,
		)
	}

	if client.ClientID != "7" {
		t.Fatalf("unexpected client id: %q", client.ClientID)
	}

	total, err := openVPNStatusTotalBytes(client)
	if err != nil {
		t.Fatal(err)
	}

	if total != 4600 {
		t.Fatalf("unexpected total bytes: %d", total)
	}
}

func TestParseOpenVPNStatusV3FallsBackToCommonName(t *testing.T) {
	raw := "" +
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\n" +
		"CLIENT_LIST\tbob\t198.51.100.20:40000\t10.66.0.11\t\t10\t20\tnow\t123\tUNDEF\t9\n"

	clients, err := parseOpenVPNStatusV3(raw)
	if err != nil {
		t.Fatal(err)
	}

	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}

	if clients[0].Username != "bob" {
		t.Fatalf(
			"expected common-name fallback, got %q",
			clients[0].Username,
		)
	}
}

func TestParseOpenVPNStatusV3RejectsInvalidCounters(t *testing.T) {
	raw := "" +
		"HEADER\tCLIENT_LIST\tCommon Name\tBytes Received\tBytes Sent\tUsername\n" +
		"CLIENT_LIST\talice\tbad\t20\talice\n"

	if _, err := parseOpenVPNStatusV3(raw); err == nil {
		t.Fatal("expected invalid counter error")
	}
}

func TestOpenVPNStatusSessionKeyUsesClientID(t *testing.T) {
	client := openVPNStatusClient{
		Username:       "alice",
		RealAddress:    "203.0.113.10:54321",
		VirtualAddress: "10.66.0.10",
		ClientID:       "7",
		ConnectedSince: "123",
	}

	first := openVPNStatusSessionKey("openvpn-main", client)

	client.RealAddress = "203.0.113.99:60000"
	client.VirtualAddress = "10.66.0.99"

	second := openVPNStatusSessionKey("openvpn-main", client)

	if first != second {
		t.Fatalf(
			"client ID session key changed: %q != %q",
			first,
			second,
		)
	}
}
