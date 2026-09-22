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
