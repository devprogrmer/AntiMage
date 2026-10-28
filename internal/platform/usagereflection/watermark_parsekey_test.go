package usagereflection

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// parseKeyReference is the original decoder implementation kept verbatim so the
// fast path in ParseKey is proven equivalent rather than merely plausible.
func parseKeyReference(key string) (string, string, error) {
	var a, b string
	decoder := json.NewDecoder(strings.NewReader(key))
	if err := decoder.Decode(&a); err != nil {
		return "", "", err
	}
	offset := int(decoder.InputOffset())
	if offset >= len(key) || key[offset] != ':' {
		return "", "", fmt.Errorf("invalid usage owner key")
	}
	if err := json.Unmarshal([]byte(key[offset+1:]), &b); err != nil {
		return "", "", err
	}
	return a, b, nil
}

func TestParseKeyMatchesDecoderReference(t *testing.T) {
	keys := []string{
		Key("xray", "vless-in"),
		Key("wireguard", "wg-in"),
		Key("openvpn", `in"bound`),
		Key("l2tp", `back\slash`),
		Key("pptp", "tab\tinbound"),
		Key("ikev2", "new\nline"),
		Key("anyconnect", "درگاه-فارسی"),
		Key("shadowsocks", "emoji-🌐"),
		Key("amneziawg", "control\x01byte"),
		Key("openvpn", ""),
		Key("", "tag-only"),
		Key(" ", "  "),
		Key("xray", "in:vless"),
	}
	for _, key := range keys {
		wantA, wantB, wantErr := parseKeyReference(key)
		gotA, gotB, gotErr := ParseKey(key)
		if (wantErr == nil) != (gotErr == nil) || wantA != gotA || wantB != gotB {
			t.Fatalf(
				"ParseKey(%q) = (%q, %q, %v); reference = (%q, %q, %v)",
				key, gotA, gotB, gotErr, wantA, wantB, wantErr,
			)
		}
	}
}

func TestParseKeyRejectsMalformedOwnerKeys(t *testing.T) {
	// Every malformed shape must still be rejected; the fast path may only be
	// used where it is byte-for-byte equivalent to the decoder fallback.
	malformed := []string{
		"",
		":",
		"plain",
		"plain:plain",
		`"xray":`,
		`"xray":in`,
		`"xray":"vless" trailing`,
		`"xray" :"vless"`,
		`"unterminated`,
		`"xray":"unterminated`,
		`"xray":"vless":extra`,
	}
	for _, key := range malformed {
		wantA, wantB, wantErr := parseKeyReference(key)
		gotA, gotB, gotErr := ParseKey(key)
		if (wantErr == nil) != (gotErr == nil) || wantA != gotA || wantB != gotB {
			t.Fatalf(
				"ParseKey(%q) = (%q, %q, %v); reference = (%q, %q, %v)",
				key, gotA, gotB, gotErr, wantA, wantB, wantErr,
			)
		}
		if gotErr == nil {
			t.Fatalf("ParseKey(%q) unexpectedly accepted a malformed owner key", key)
		}
	}
}

func BenchmarkParseKeyProductionShape(b *testing.B) {
	key := Key("xray", "vless-in-0007")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := ParseKey(key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseKeyDecoderReference(b *testing.B) {
	key := Key("xray", "vless-in-0007")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := parseKeyReference(key); err != nil {
			b.Fatal(err)
		}
	}
}
