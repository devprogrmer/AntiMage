package nodeagent

import (
	"context"
	"fmt"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeUserOperationsDoNotAcknowledgeNoOp(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	_, add := s.AddUser(context.Background(), &nodev1.InboundUserRequest{})
	_, update := s.UpdateUser(context.Background(), &nodev1.InboundUserRequest{})
	_, remove := s.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{})
	for name, err := range map[string]error{"add": add, "update": update, "remove": remove} {
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("%s silently accepted a no-op: %v", name, err)
		}
	}
}

func TestXrayLiveUserAdditionsPlan(t *testing.T) {
	config := func(clients string) string {
		return fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"decryption":"none","clients":%s}}]}`, clients)
	}
	old := config(`[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}]`)
	next := config(`[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"two","id":"22222222-2222-4222-8222-222222222222","level":0}]`)
	adds, eligible, err := xrayLiveUserAdditions(old, next)
	if err != nil || !eligible || len(adds) != 1 || adds[0].tag != "vless" || adds[0].user.Email != "two" {
		t.Fatalf("plan: %+v eligible=%v err=%v", adds, eligible, err)
	}
	adds, eligible, err = xrayLiveUserAdditions(old, old)
	if err != nil || !eligible || len(adds) != 0 {
		t.Fatal("identical runtime should not restart")
	}
}

func TestXrayLiveShadowsocksAdditions(t *testing.T) {
	for _, tc := range []struct{ name, settings, oldClient, newClient string }{
		{"aead", ``, `{"email":"one","password":"one-secret","method":"aes-128-gcm","iv_check":false}`, `{"email":"two","password":"two-secret","method":"aes-128-gcm","iv_check":false}`},
		{"2022", `"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA==",`, `{"email":"one","password":"AQEBAQEBAQEBAQEBAQEBAQ=="}`, `{"email":"two","password":"AgICAgICAgICAgICAgICAg=="}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := func(clients string) string {
				return fmt.Sprintf(`{"inbounds":[{"tag":"ss","protocol":"shadowsocks","settings":{%s"clients":[%s]}}]}`, tc.settings, clients)
			}
			adds, eligible, err := xrayLiveUserAdditions(config(tc.oldClient), config(tc.oldClient+","+tc.newClient))
			if err != nil || !eligible || len(adds) != 1 || adds[0].user.Email != "two" {
				t.Fatalf("SS plan=%v eligible=%v err=%v", adds, eligible, err)
			}
		})
	}
}

func TestXrayLiveShadowsocksRejectsLegacySingleUserTransition(t *testing.T) {
	for _, method := range []string{"aes-128-gcm", "2022-blake3-aes-128-gcm"} {
		old := fmt.Sprintf(`{"inbounds":[{"tag":"ss","protocol":"shadowsocks","settings":{"method":%q,"password":"AAAAAAAAAAAAAAAAAAAAAA=="}}]}`, method)
		next := fmt.Sprintf(`{"inbounds":[{"tag":"ss","protocol":"shadowsocks","settings":{"method":%q,"password":"AAAAAAAAAAAAAAAAAAAAAA==","clients":[{"email":"two","password":"AgICAgICAgICAgICAgICAg=="}]}}]}`, method)
		if _, eligible, err := xrayLiveUserAdditions(old, next); err != nil || eligible {
			t.Fatal("legacy credential would survive transition to multi-user")
		}
	}
}

func TestXrayLiveHysteriaRejectsLegacyStaticAuthTransition(t *testing.T) {
	old := `{"inbounds":[{"tag":"hy2","protocol":"hysteria","settings":{"version":2},"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":2,"auth":"legacy-secret"}}}]}`
	next := `{"inbounds":[{"tag":"hy2","protocol":"hysteria","settings":{"version":2,"clients":[{"email":"two","auth":"two-secret"}]},"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":2,"auth":"legacy-secret"}}}]}`
	if _, eligible, err := xrayLiveUserAdditions(old, next); err != nil || eligible {
		t.Fatalf("live user would disable existing static auth: eligible=%v err=%v", eligible, err)
	}
}

func TestXrayLiveHysteriaAddition(t *testing.T) {
	old := `{"inbounds":[{"tag":"hy2","protocol":"hysteria","settings":{"clients":[{"email":"one","auth":"one-secret"}]}}]}`
	next := `{"inbounds":[{"tag":"hy2","protocol":"hysteria","settings":{"clients":[{"email":"one","auth":"one-secret"},{"email":"two","auth":"two-secret"}]}}]}`
	adds, eligible, err := xrayLiveUserAdditions(old, next)
	if err != nil || !eligible || len(adds) != 1 || adds[0].user.AccountType != "xray.proxy.hysteria.account.Account" {
		t.Fatalf("Hysteria addition not available: %v %v %v", adds, eligible, err)
	}
}

func TestXrayLiveUserAdditionsRejectCredentialCollision(t *testing.T) {
	for _, tc := range []struct{ name, protocol, oldClient, newClient string }{
		{"vless", "vless", `{"email":"one","id":"11111111-1111-4111-8111-111111111111"}`, `{"email":"two","id":"11111111111141118111111111111111"}`},
		{"trojan", "trojan", `{"email":"one","password":"shared-secret"}`, `{"email":"two","password":"shared-secret"}`},
		{"hysteria", "hysteria", `{"email":"one","auth":"shared-secret"}`, `{"email":"two","auth":"shared-secret"}`},
		{"hysteria-uuid-version", "hysteria", `{"email":"one","auth":"11111111-1111-4111-8111-111111111111"}`, `{"email":"two","auth":"11111111-1111-4222-8111-111111111111"}`},
		{"shadowsocks", "shadowsocks", `{"email":"one","password":"shared-secret","method":"aes-128-gcm"}`, `{"email":"two","password":"shared-secret","method":"aes-128-gcm"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := func(clients string) string {
				return fmt.Sprintf(`{"inbounds":[{"tag":"users","protocol":%q,"settings":{"clients":[%s]}}]}`, tc.protocol, clients)
			}
			_, eligible, err := xrayLiveUserAdditions(config(tc.oldClient), config(tc.oldClient+","+tc.newClient))
			if err != nil || eligible {
				t.Fatalf("credential collision could steal existing traffic: eligible=%v err=%v", eligible, err)
			}
		})
	}
}

func TestXrayLiveShadowsocks2022RejectsEquivalentBase64Keys(t *testing.T) {
	config := func(clients string) string {
		return fmt.Sprintf(`{"inbounds":[{"tag":"ss","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA==","clients":[%s]}}]}`, clients)
	}
	oldClient := `{"email":"one","password":"AQEBAQEBAQEBAQEBAQEBAQ=="}`
	alias := `{"email":"two","password":"AQEB\nAQEBAQEBAQEBAQEBAQ=="}`
	if _, eligible, err := xrayLiveUserAdditions(config(oldClient), config(oldClient+","+alias)); err != nil || eligible {
		t.Fatalf("base64 alias could overwrite an existing SS2022 user: eligible=%v err=%v", eligible, err)
	}
}

func TestXrayLiveUserAdditionsRefusesStructuralOrRevocationChanges(t *testing.T) {
	old := `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}],"decryption":"none"}}]}`
	for name, next := range map[string]string{
		"remove":                `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"}}]}`,
		"credential-update":     `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"changed"}],"decryption":"none"}}]}`,
		"port-change":           `{"inbounds":[{"tag":"vless","port":8443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}],"decryption":"none"}}]}`,
		"unknown-account-field": `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"two","id":"22222222-2222-4222-8222-222222222222","reverse":{"tag":"reverse"}}],"decryption":"none"}}]}`,
		"duplicate-email":       `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"one","id":"22222222-2222-4222-8222-222222222222"}],"decryption":"none"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, eligible, err := xrayLiveUserAdditions(old, next)
			if err != nil || eligible {
				t.Fatalf("unsafe live plan: eligible=%v err=%v", eligible, err)
			}
		})
	}
}
