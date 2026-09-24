package user

import "testing"

func TestSubscriptionMetadataFromAndroidUserAgent(t *testing.T) {
	meta := subscriptionMetadataFromRequest(
		SubscriptionRenderRequest{
			UserAgent: "Mozilla/5.0 (Linux; Android 15; SM-S928B Build/AP3A.240905.015; wv) AppleWebKit/537.36",
		},
		"wg",
	)

	if meta.OSName != "Android" || meta.OSVersion != "15" {
		t.Fatalf("unexpected Android OS metadata: %#v", meta)
	}
	if meta.Model != "SM-S928B" {
		t.Fatalf("model=%q", meta.Model)
	}
	if meta.ClientName != "WireGuard" {
		t.Fatalf("client=%q", meta.ClientName)
	}
}

func TestSubscriptionMetadataPrefersExplicitClientHints(t *testing.T) {
	meta := subscriptionMetadataFromRequest(
		SubscriptionRenderRequest{
			UserAgent:             "Mozilla/5.0",
			DeviceType:            "Mobile",
			DeviceManufacturer:    "Samsung",
			DeviceModel:           "SM-S928B",
			DevicePlatform:        "Android",
			DevicePlatformVersion: "15",
			DeviceClientName:      "AntiMage",
			DeviceClientVersion:   "1.2.3",
		},
		"wg",
	)

	if meta.Manufacturer != "Samsung" ||
		meta.Model != "SM-S928B" ||
		meta.OSName != "Android" ||
		meta.OSVersion != "15" ||
		meta.ClientName != "AntiMage" ||
		meta.ClientVersion != "1.2.3" {
		t.Fatalf("unexpected explicit metadata: %#v", meta)
	}
}

func TestStableWGDeviceIDIsStableAndOpaque(t *testing.T) {
	first := stableWGDeviceID("public-key-a")
	second := stableWGDeviceID("public-key-a")
	other := stableWGDeviceID("public-key-b")

	if first == "" || first != second || first == other {
		t.Fatalf("invalid stable IDs: first=%q second=%q other=%q", first, second, other)
	}
}
func TestGenericSubscriptionDeviceID(t *testing.T) {
	req := SubscriptionRenderRequest{
		UserAgent: "Mozilla/5.0 (Linux; Android 11; SM-A505F Build/RP1A.200720.012)",
		ClientIP:  "198.51.100.10",
	}

	meta := subscriptionMetadataFromRequest(
		req,
		"openvpn",
	)

	first := genericSubscriptionDeviceID(
		"openvpn",
		"ov-main",
		req,
		meta,
	)

	second := genericSubscriptionDeviceID(
		"ov",
		"ov-main",
		req,
		meta,
	)

	if first == "" || first != second {
		t.Fatalf(
			"unstable generic device id: first=%q second=%q",
			first,
			second,
		)
	}

	if meta.Manufacturer != "Samsung" {
		t.Fatalf(
			"manufacturer=%q want Samsung",
			meta.Manufacturer,
		)
	}

	if meta.Model != "SM-A505F" {
		t.Fatalf(
			"model=%q want SM-A505F",
			meta.Model,
		)
	}
}

func TestMetadataClientNames(t *testing.T) {
	tests := map[string]string{
		"openvpn":    "OpenVPN",
		"l2tp":       "L2TP",
		"pptp":       "PPTP",
		"ikev2":      "IKEv2",
		"anyconnect": "Cisco AnyConnect",
		"xray":       "Xray",
		"amneziawg":  "AmneziaWG",
	}

	for protocol, want := range tests {
		if got := metadataClientName(protocol); got != want {
			t.Fatalf(
				"%s client=%q want %q",
				protocol,
				got,
				want,
			)
		}
	}
}
