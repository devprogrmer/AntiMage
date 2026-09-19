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

func TestRegistryMarksNonConstructibleWireGuardVariants(t *testing.T) {
	for _, id := range []string{"wg-c"} {
		definition, ok := Find(id)
		if !ok {
			t.Fatalf("protocol %q is missing from the AntiMage registry", id)
		}
		if definition.Constructible == nil || *definition.Constructible {
			t.Fatalf("protocol %q should be explicitly non-constructible", id)
		}
		if definition.Inbound || definition.Subscription || definition.TrafficAccounting {
			t.Fatalf("protocol %q exposes constructible inbound capabilities: %#v", id, definition)
		}
	}

	awg, ok := Find("amneziawg")
	if !ok {
		t.Fatal("amneziawg is missing from the AntiMage registry")
	}
	if awg.Constructible != nil && !*awg.Constructible {
		t.Fatalf("amneziawg must be constructible: %#v", awg)
	}
	if !awg.Inbound || !awg.Subscription || !awg.TrafficAccounting {
		t.Fatalf("amneziawg capabilities are incomplete: %#v", awg)
	}
	if awg.DefaultPort != 51821 || awg.Network != "udp" {
		t.Fatalf("amneziawg listener defaults = %d/%s, want 51821/udp", awg.DefaultPort, awg.Network)
	}

	definition, ok := Find("wireguard")
	if !ok {
		t.Fatal("wireguard is missing from the AntiMage registry")
	}
	if definition.Constructible != nil {
		t.Fatalf("wireguard should use the default constructible capability, got %#v", definition.Constructible)
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
