package nodecontroller

import "testing"

func TestNormalizeNodeUpdateChannel(t *testing.T) {
	tests := map[string]string{
		"":            "",
		"stable":      "stable",
		"latest":      "stable",
		"release":     "stable",
		"master":      "stable",
		"dev":         "dev",
		"development": "dev",
		"dev-builds":  "dev",
	}
	for input, want := range tests {
		if got := normalizeNodeUpdateChannel(input); got != want {
			t.Errorf("normalizeNodeUpdateChannel(%q) = %q, want %q", input, got, want)
		}
	}
}
