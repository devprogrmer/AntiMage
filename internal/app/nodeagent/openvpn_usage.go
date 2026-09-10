package nodeagent

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type openVPNStatusClient struct {
	Username       string
	CommonName     string
	RealAddress    string
	ClientIP       string
	VirtualAddress string
	ClientID       string
	ConnectedSince string
	BytesReceived  uint64
	BytesSent      uint64
}

func parseOpenVPNStatusV3(raw string) ([]openVPNStatusClient, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	var header map[string]int
	var clients []openVPNStatusClient

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		switch parts[0] {
		case "HEADER":
			if parts[1] != "CLIENT_LIST" {
				continue
			}

			header = make(map[string]int, len(parts)-2)
			for i := 2; i < len(parts); i++ {
				header[strings.TrimSpace(parts[i])] = i - 2
			}

		case "CLIENT_LIST":
			if header == nil {
				return nil, fmt.Errorf(
					"openvpn status CLIENT_LIST appeared before HEADER",
				)
			}

			values := parts[1:]

			field := func(name string) string {
				index, ok := header[name]
				if !ok || index < 0 || index >= len(values) {
					return ""
				}
				return strings.TrimSpace(values[index])
			}

			commonName := field("Common Name")
			username := field("Username")

			if username == "" || strings.EqualFold(username, "UNDEF") {
				username = commonName
			}

			if username == "" || strings.EqualFold(username, "UNDEF") {
				continue
			}

			received, err := parseOpenVPNStatusBytes(
				field("Bytes Received"),
			)
			if err != nil {
				return nil, fmt.Errorf(
					"openvpn status user %q bytes received: %w",
					username,
					err,
				)
			}

			sent, err := parseOpenVPNStatusBytes(
				field("Bytes Sent"),
			)
			if err != nil {
				return nil, fmt.Errorf(
					"openvpn status user %q bytes sent: %w",
					username,
					err,
				)
			}

			realAddress := field("Real Address")

			clients = append(
				clients,
				openVPNStatusClient{
					Username:       username,
					CommonName:     commonName,
					RealAddress:    realAddress,
					ClientIP:       openVPNRealAddressIP(realAddress),
					VirtualAddress: field("Virtual Address"),
					ClientID:       field("Client ID"),
					ConnectedSince: field("Connected Since (time_t)"),
					BytesReceived:  received,
					BytesSent:      sent,
				},
			)
		}
	}

	return clients, nil
}

func parseOpenVPNStatusBytes(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}

	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte counter %q", raw)
	}

	return value, nil
}

func openVPNRealAddressIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		return strings.TrimSpace(host)
	}

	// Some OpenVPN builds/status outputs can contain an address
	// without a port.
	return raw
}

func openVPNStatusSessionKey(
	inboundTag string,
	client openVPNStatusClient,
) string {
	if strings.TrimSpace(client.ClientID) != "" {
		return strings.TrimSpace(inboundTag) +
			"\x00client-id\x00" +
			strings.TrimSpace(client.ClientID)
	}

	return strings.Join(
		[]string{
			strings.TrimSpace(inboundTag),
			strings.TrimSpace(client.Username),
			strings.TrimSpace(client.RealAddress),
			strings.TrimSpace(client.VirtualAddress),
			strings.TrimSpace(client.ConnectedSince),
		},
		"\x00",
	)
}

func openVPNStatusTotalBytes(
	client openVPNStatusClient,
) (uint64, error) {
	if ^uint64(0)-client.BytesReceived < client.BytesSent {
		return 0, fmt.Errorf("openvpn byte counter overflow")
	}

	return client.BytesReceived + client.BytesSent, nil
}
