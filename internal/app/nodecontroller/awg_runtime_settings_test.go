package nodecontroller

import "testing"

func TestAWGRuntimeSettingsNormalizesPanelAddressPool(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings map[string]any
		want     string
	}{
		{name: "panel snake case", settings: map[string]any{"ipv4_pool_cidr": "10.73.0.0/24"}, want: "10.73.0.0/24"},
		{name: "panel camel case", settings: map[string]any{"ipv4PoolCidr": "10.74.0.0/24"}, want: "10.74.0.0/24"},
		{name: "runtime field wins", settings: map[string]any{"address_pool": "10.75.0.0/24", "ipv4_pool_cidr": "10.73.0.0/24"}, want: "10.75.0.0/24"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := AWGRuntimeSettings(map[string]any{"settings": test.settings})
			if got["address_pool"] != test.want {
				t.Fatalf("address_pool = %v, want %q", got["address_pool"], test.want)
			}
		})
	}
}
