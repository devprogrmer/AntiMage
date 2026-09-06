package protocols

import "testing"

func TestRegistryIncludesBothSourceFamilies(t *testing.T) {
	for _, id := range []string{"vless", "hysteria", "http", "openvpn", "wireguard", "wg-c", "awg", "gre", "anyconnect", "anytls", "naiveproxy"} {
		if _, ok := Find(id); !ok {
			t.Fatalf("protocol %q is missing from the AntiMage registry", id)
		}
	}
}

func TestAllReturnsIndependentSlice(t *testing.T) {
	definitions := All()
	definitions[0].ID = "changed"

	definition, ok := Find("vmess")
	if !ok || definition.ID != "vmess" {
		t.Fatal("mutating All result changed the registry")
	}
}

func TestCanonicalIDAcceptsUpstreamAliases(t *testing.T) {
	for alias, want := range map[string]string{
		"naive": "naiveproxy",
		" AWG ": "amneziawg",
	} {
		if got := CanonicalID(alias); got != want {
			t.Fatalf("CanonicalID(%q)=%q want %q", alias, got, want)
		}
		if _, ok := Find(alias); !ok {
			t.Fatalf("alias %q did not resolve", alias)
		}
	}
}

func TestRegistryIDsAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, exists := seen[definition.ID]; exists {
			t.Fatalf("duplicate protocol ID %q", definition.ID)
		}
		seen[definition.ID] = struct{}{}
	}
}
