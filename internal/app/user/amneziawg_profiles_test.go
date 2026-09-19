package user

import (
	"strings"
	"testing"
)

func TestRenderAmneziaWGProfilesIncludesEveryDeviceAndObfuscationValue(t *testing.T) {
	profiles, err := RenderAmneziaWGProfiles(AWGProfileRequest{
		Username: "alice", Endpoint: "vpn.example.com:51821", ServerPublicKey: "server-key",
		DNS: []string{"1.1.1.1"}, MTU: 1420, PersistentKeepalive: 25,
		Jc: 4, Jmin: 8, Jmax: 80, S1: 77, S2: 90,
		H1: "1234567", H2: "2345678", H3: "3456789", H4: "4567890",
		Devices: []AWGDevice{
			{DeviceIndex: 0, PrivateKey: "private-a", PresharedKey: "psk-a", Address: "10.72.0.2"},
			{DeviceIndex: 1, PrivateKey: "private-b", PresharedKey: "psk-b", Address: "10.72.0.3"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles=%d want 2", len(profiles))
	}
	if profiles[0].Body == profiles[1].Body {
		t.Fatal("per-device profiles are identical")
	}
	for _, profile := range profiles {
		for _, line := range []string{"[Interface]", "Address = 10.72.0.", "Jc = 4", "Jmin = 8", "Jmax = 80", "S1 = 77", "S2 = 90", "H1 = 1234567", "H4 = 4567890", "[Peer]", "PublicKey = server-key", "Endpoint = vpn.example.com:51821", "AllowedIPs = 0.0.0.0/0"} {
			if !strings.Contains(profile.Body, line) {
				t.Fatalf("profile missing %q:\n%s", line, profile.Body)
			}
		}
	}
}
