package user

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

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

func TestGenericSubscriptionDeviceIDUsesClientHWID(t *testing.T) {
	firstRequest := SubscriptionRenderRequest{DeviceHWID: "device-a", UserAgent: "Happ/1.0"}
	secondRequest := SubscriptionRenderRequest{DeviceHWID: "device-b", UserAgent: "Happ/1.0"}
	firstMeta := subscriptionMetadataFromRequest(firstRequest, "xray")
	secondMeta := subscriptionMetadataFromRequest(secondRequest, "xray")
	first := genericSubscriptionDeviceID("xray", "", firstRequest, firstMeta)
	firstAgain := genericSubscriptionDeviceID("xray", "", firstRequest, firstMeta)
	second := genericSubscriptionDeviceID("xray", "", secondRequest, secondMeta)
	if first == "" || first != firstAgain || first == second {
		t.Fatalf("HWID device IDs are not stable and distinct: first=%q again=%q second=%q", first, firstAgain, second)
	}
}

func TestRegisterXraySubscriptionHWIDEnforcesSlotsIdempotently(t *testing.T) {
	db, err := sql.Open("sqlite", "file:xray-hwid-slots?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, device_limit INTEGER NOT NULL)`,
		`INSERT INTO users (id, device_limit) VALUES (7, 2)`,
		`CREATE TABLE vpn_device_metadata (user_id INTEGER NOT NULL, protocol TEXT NOT NULL, inbound_tag TEXT NOT NULL DEFAULT '', device_id TEXT NOT NULL, device_type TEXT NOT NULL DEFAULT 'Unknown', manufacturer TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', os_name TEXT NOT NULL DEFAULT '', os_version TEXT NOT NULL DEFAULT '', client_name TEXT NOT NULL DEFAULT 'Unknown', client_version TEXT NOT NULL DEFAULT '', platform TEXT NOT NULL DEFAULT 'Unknown', metadata_source TEXT NOT NULL DEFAULT 'subscription', client_ip TEXT NOT NULL DEFAULT '', last_seen_at DATETIME NOT NULL, PRIMARY KEY (user_id, protocol, inbound_tag, device_id))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(NewRepository(db, "sqlite"))
	user := UserDetail{ID: 7, DeviceLimit: 2}
	for _, hwid := range []string{"phone-a", "phone-a", "phone-b"} {
		if err := service.registerXraySubscriptionHWID(context.Background(), user, SubscriptionRenderRequest{DeviceHWID: hwid}); err != nil {
			t.Fatalf("register %q: %v", hwid, err)
		}
	}
	if err := service.registerXraySubscriptionHWID(context.Background(), user, SubscriptionRenderRequest{DeviceHWID: "phone-c"}); err == nil {
		t.Fatal("third HWID unexpectedly received a subscription slot")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vpn_device_metadata WHERE user_id=7 AND protocol='xray' AND device_id LIKE 'xray-hwid-%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("registered HWID slots=%d, want 2", count)
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
