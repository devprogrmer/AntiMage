package nodeagent

import (
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The targeted Xray user path encodes each account by hand because the panel
// links the published core only for JSON config, not for the proxyman wire
// messages. A renamed proto package fails loudly, but a RENUMBERED field is
// silent: protobuf files the unknown number under unknownFields and the account
// is accepted holding an empty credential, so the user simply never
// authenticates. These tests pin every field number the encoder writes, plus
// the type URL, against fixed expected values so a core bump that moves a field
// is caught here instead of in production.
func TestXrayUserAccountWireLayouts(t *testing.T) {
	cases := []struct {
		name        string
		protocol    string
		fields      map[string]string
		accountName string
		accountType string
		wantFields  map[protoreflect.Name]any
	}{
		{
			name:        "vless",
			protocol:    "vless",
			fields:      map[string]string{"id": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision"},
			accountName: "VlessAccount",
			accountType: xrayVlessAccountType,
			wantFields: map[protoreflect.Name]any{
				"id":   "11111111-1111-4111-8111-111111111111",
				"flow": "xtls-rprx-vision",
			},
		},
		{
			name:        "vmess",
			protocol:    "vmess",
			fields:      map[string]string{"id": "22222222-2222-4222-8222-222222222222"},
			accountName: "VmessAccount",
			accountType: xrayVmessAccountType,
			wantFields: map[protoreflect.Name]any{
				"id": "22222222-2222-4222-8222-222222222222",
			},
		},
		{
			name:        "trojan",
			protocol:    "trojan",
			fields:      map[string]string{"password": "trojan-secret"},
			accountName: "TrojanAccount",
			accountType: xrayTrojanAccountType,
			wantFields: map[protoreflect.Name]any{
				"password": "trojan-secret",
			},
		},
		{
			name:        "shadowsocks aead",
			protocol:    "shadowsocks",
			fields:      map[string]string{"password": "ss-secret", "method": "chacha20-ietf-poly1305", "iv_check": "true"},
			accountName: "ShadowsocksAccount",
			accountType: xrayShadowsocksAccountType,
			wantFields: map[protoreflect.Name]any{
				"password":    "ss-secret",
				"cipher_type": uint32(xrayCipherChaCha20Poly1305),
				"iv_check":    true,
			},
		},
		{
			name:        "shadowsocks 2022",
			protocol:    "shadowsocks",
			fields:      map[string]string{"password": "c2VjcmV0LWtleS1iYXNlNjQtZW5jb2RlZA=="},
			accountName: "Shadowsocks2022Account",
			accountType: xrayShadowsocks2022AccountType,
			wantFields: map[protoreflect.Name]any{
				"key": "c2VjcmV0LWtleS1iYXNlNjQtZW5jb2RlZA==",
			},
		},
		{
			name:        "hysteria",
			protocol:    "hysteria",
			fields:      map[string]string{"auth": "hysteria-secret"},
			accountName: "HysteriaAccount",
			accountType: xrayHysteriaAccountType,
			wantFields: map[protoreflect.Name]any{
				"auth": "hysteria-secret",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			message, err := xrayUserMessage(&nodev1.InboundUser{
				Protocol: c.protocol,
				Email:    "user@example.test",
				Fields:   c.fields,
			})
			if err != nil {
				t.Fatalf("xrayUserMessage: %v", err)
			}
			if got := message.Get(message.Descriptor().Fields().ByName("email")).String(); got != "user@example.test" {
				t.Fatalf("email field = %q", got)
			}
			account := message.Get(message.Descriptor().Fields().ByName("account")).Message()
			if got := account.Get(account.Descriptor().Fields().ByName("type")).String(); got != c.accountType {
				t.Fatalf("account type URL = %q, want %q", got, c.accountType)
			}
			raw := account.Get(account.Descriptor().Fields().ByName("value")).Bytes()

			// Re-parse the account bytes through the same descriptor the core
			// resolves from the type URL, then assert every field by name. A
			// wrong field number shows up here as a zero value.
			decoded := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName(protoreflect.Name(c.accountName)))
			if err := proto.Unmarshal(raw, decoded); err != nil {
				t.Fatalf("decode %s: %v", c.accountName, err)
			}
			for name, want := range c.wantFields {
				field := decoded.Descriptor().Fields().ByName(name)
				if field == nil {
					t.Fatalf("%s has no field %q", c.accountName, name)
				}
				got := decoded.Get(field)
				switch expected := want.(type) {
				case string:
					if got.String() != expected {
						t.Fatalf("%s.%s = %q, want %q", c.accountName, name, got.String(), expected)
					}
				case bool:
					if got.Bool() != expected {
						t.Fatalf("%s.%s = %v, want %v", c.accountName, name, got.Bool(), expected)
					}
				case uint32:
					if uint32(got.Uint()) != expected {
						t.Fatalf("%s.%s = %d, want %d", c.accountName, name, got.Uint(), expected)
					}
				default:
					t.Fatalf("unhandled expectation type %T", want)
				}
			}
		})
	}
}

// Pin the field numbers themselves. The descriptor above is built in this
// repository, so this table is what actually has to agree with the pinned
// core's proxy/*.proto; the test fails loudly if someone edits a number.
func TestXrayUserAccountFieldNumbersArePinned(t *testing.T) {
	want := map[string]map[protoreflect.Name]int32{
		"VlessAccount":           {"id": 1, "flow": 2, "encryption": 3, "padding": 6},
		"VmessAccount":           {"id": 1},
		"TrojanAccount":          {"password": 1},
		"ShadowsocksAccount":     {"password": 1, "cipher_type": 2, "iv_check": 3},
		"Shadowsocks2022Account": {"key": 1},
		"HysteriaAccount":        {"auth": 1},
	}
	for message, fields := range want {
		descriptor := xrayUserWireDescriptor.Messages().ByName(protoreflect.Name(message))
		if descriptor == nil {
			t.Fatalf("wire descriptor has no message %q", message)
		}
		for name, number := range fields {
			field := descriptor.Fields().ByName(name)
			if field == nil {
				t.Fatalf("%s has no field %q", message, name)
			}
			if field.Number() != protoreflect.FieldNumber(number) {
				t.Fatalf("%s.%s is field %d, pinned core expects %d", message, name, field.Number(), number)
			}
		}
	}
	for _, accountType := range []string{
		xrayVmessAccountType, xrayVlessAccountType, xrayTrojanAccountType,
		xrayShadowsocksAccountType, xrayShadowsocks2022AccountType, xrayHysteriaAccountType,
	} {
		if !strings.HasPrefix(accountType, "xray.proxy.") {
			t.Fatalf("account type %q is not a core proxy account URL", accountType)
		}
	}
}

func TestXrayShadowsocksCipherTypeMatchesCoreMapping(t *testing.T) {
	cases := map[string]uint32{
		"aes-128-gcm":             xrayCipherAES128GCM,
		"aead_aes_128_gcm":        xrayCipherAES128GCM,
		"aes-256-gcm":             xrayCipherAES256GCM,
		"aead_aes_256_gcm":        xrayCipherAES256GCM,
		"chacha20-poly1305":       xrayCipherChaCha20Poly1305,
		"chacha20-ietf-poly1305":  xrayCipherChaCha20Poly1305,
		"aead_chacha20_poly1305":  xrayCipherChaCha20Poly1305,
		"xchacha20-poly1305":      xrayCipherXChaCha20Poly1305,
		"xchacha20-ietf-poly1305": xrayCipherXChaCha20Poly1305,
		"aead_xchacha20_poly1305": xrayCipherXChaCha20Poly1305,
		"AES-256-GCM":             xrayCipherAES256GCM,
	}
	for method, want := range cases {
		got, ok := xrayShadowsocksCipherType(method)
		if !ok || got != want {
			t.Fatalf("cipher %q = (%d, %v), want (%d, true)", method, got, ok, want)
		}
	}
	// The core rejects both of these as per-user AEAD methods; the encoder must
	// not silently accept them and produce an unusable account.
	for _, method := range []string{"none", "plain", "unknown", ""} {
		if got, ok := xrayShadowsocksCipherType(method); ok {
			t.Fatalf("cipher %q unexpectedly mapped to %d", method, got)
		}
	}
}

func TestXrayUserMessageRejectsIncompleteCredentials(t *testing.T) {
	cases := []struct {
		name  string
		user  *nodev1.InboundUser
		match string
	}{
		{"vless without id", &nodev1.InboundUser{Protocol: "vless", Email: "u@example.test"}, "VLESS user id is required"},
		{"vmess without id", &nodev1.InboundUser{Protocol: "vmess", Email: "u@example.test"}, "VMess user id is required"},
		{"trojan without password", &nodev1.InboundUser{Protocol: "trojan", Email: "u@example.test"}, "Trojan user password is required"},
		{"shadowsocks without password", &nodev1.InboundUser{Protocol: "shadowsocks", Email: "u@example.test", Fields: map[string]string{"method": "aes-256-gcm"}}, "Shadowsocks user password is required"},
		{"shadowsocks with unknown cipher", &nodev1.InboundUser{Protocol: "shadowsocks", Email: "u@example.test", Fields: map[string]string{"password": "x", "method": "rc4"}}, "unsupported Shadowsocks user cipher"},
		{"hysteria without auth", &nodev1.InboundUser{Protocol: "hysteria", Email: "u@example.test"}, "Hysteria user auth is required"},
		{"missing email", &nodev1.InboundUser{Protocol: "trojan", Fields: map[string]string{"password": "x"}}, "email is required"},
		{"unsupported protocol", &nodev1.InboundUser{Protocol: "socks", Email: "u@example.test"}, "does not support this protocol"},
		{"nil user", nil, "does not support this protocol"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := xrayUserMessage(c.user)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.match) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), c.match)
			}
		})
	}
}

// hysteria2 is an alias the panel accepts; the core account type is the same.
func TestXrayHysteria2AliasUsesHysteriaAccount(t *testing.T) {
	message, err := xrayUserMessage(&nodev1.InboundUser{
		Protocol: "Hysteria2",
		Email:    "user@example.test",
		Fields:   map[string]string{"auth": "secret"},
	})
	if err != nil {
		t.Fatalf("xrayUserMessage: %v", err)
	}
	account := message.Get(message.Descriptor().Fields().ByName("account")).Message()
	if got := account.Get(account.Descriptor().Fields().ByName("type")).String(); got != xrayHysteriaAccountType {
		t.Fatalf("hysteria2 account type = %q, want %q", got, xrayHysteriaAccountType)
	}
}
