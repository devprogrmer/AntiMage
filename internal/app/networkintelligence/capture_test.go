package networkintelligence

import "testing"

func TestCaptureRequiresExplicitInterface(t *testing.T) {
	for _, name := range []string{"", " ", "wg0/other", "wg0\x00", "interface-too-long", " wg0"} {
		if err := (CaptureConfig{Interface: name}).validate(); err == nil {
			t.Errorf("accepted interface %q", name)
		}
	}
	for _, name := range []string{"wg0", "amwg12345678901", "vpn-test.1"} {
		if err := (CaptureConfig{Interface: name}).validate(); err != nil {
			t.Errorf("rejected interface %q: %v", name, err)
		}
	}
}
