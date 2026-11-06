package nodeagent

import (
	"encoding/json"
	"fmt"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestXrayRevocationRemovesLastUserFallbackInbound(t *testing.T) {
	for _, protocol := range []string{"shadowsocks", "hysteria"} {
		for _, field := range []string{"clients", "users"} {
			for _, mode := range []string{"quota", "offline"} {
				t.Run(protocol+"/"+field+"/"+mode, func(t *testing.T) {
					input := fmt.Sprintf(`{"inbounds":[{"tag":"API","protocol":"tunnel"},{"tag":"managed","protocol":%q,"settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key",%q:[{"email":"quota-user","password":"user-key","auth":"user-key"}]}},{"tag":"other","protocol":"vless","settings":{"clients":[{"email":"other-user","id":"keep"}]}}]}`, protocol, field)
					var got string
					if mode == "quota" {
						var removed bool
						var err error
						got, removed, err = removeXrayConfigClients(input, map[string]struct{}{"quota-user": {}})
						if err != nil || !removed {
							t.Fatalf("quota removal: removed=%v err=%v", removed, err)
						}
					} else {
						s := New(Config{DataDir: t.TempDir()})
						req, err := s.guardOfflineRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: input, OvRuntimeJson: `{"xray_policies":[{"user_id":1,"email":"quota-user","inbound_tag":"managed","status":"active","used_traffic":100,"data_limit":100}]}`})
						if err != nil {
							t.Fatal(err)
						}
						got = req.ConfigJson
					}
					var cfg struct {
						Inbounds []struct {
							Tag string `json:"tag"`
						} `json:"inbounds"`
					}
					if err := json.Unmarshal([]byte(got), &cfg); err != nil {
						t.Fatal(err)
					}
					if len(cfg.Inbounds) != 2 || cfg.Inbounds[0].Tag != "API" || cfg.Inbounds[1].Tag != "other" {
						t.Fatalf("last-user revocation retained fallback auth or changed unrelated listeners: %s", got)
					}
				})
			}
		}
	}
}

func TestXrayRevocationPreservesOtherFallbackInboundUsers(t *testing.T) {
	for _, field := range []string{"clients", "users"} {
		t.Run(field, func(t *testing.T) {
			input := fmt.Sprintf(`{"inbounds":[{"tag":"managed","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key",%q:[{"email":"quota-user","password":"one"},{"email":"active-user","password":"two"}]}},{"tag":"static","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"legacy-key"}}]}`, field)
			got, removed, err := removeXrayConfigClients(input, map[string]struct{}{"quota-user": {}})
			if err != nil || !removed {
				t.Fatalf("remove: %v %v", removed, err)
			}
			var cfg struct {
				Inbounds []struct {
					Tag      string                     `json:"tag"`
					Settings map[string]json.RawMessage `json:"settings"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal([]byte(got), &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Inbounds) != 2 || cfg.Inbounds[0].Tag != "managed" || cfg.Inbounds[1].Tag != "static" {
				t.Fatalf("listeners changed: %s", got)
			}
			var users []struct {
				Email string `json:"email"`
			}
			if err := json.Unmarshal(cfg.Inbounds[0].Settings[field], &users); err != nil {
				t.Fatal(err)
			}
			if len(users) != 1 || users[0].Email != "active-user" || string(cfg.Inbounds[1].Settings["password"]) != `"legacy-key"` {
				t.Fatalf("active users or unmanaged static credential changed: %s", got)
			}
			unchanged, removed, err := removeXrayConfigClients(got, map[string]struct{}{"absent": {}})
			if err != nil || removed || unchanged != got {
				t.Fatal("absent revocation changed config")
			}
		})
	}
}

func TestOfflineGuardRemovesAlreadyEmptyManagedFallback(t *testing.T) {
	for _, settings := range []string{``, `,"settings":{}`, `,"settings":{"clients":[]}`, `,"settings":{"clients":null}`} {
		t.Run(settings, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			config := `{"inbounds":[{"tag":"managed","protocol":"hysteria"` + settings + `,"streamSettings":{"hysteriaSettings":{"auth":"shared-key"}}},{"tag":"static","protocol":"hysteria","streamSettings":{"hysteriaSettings":{"auth":"intentional-static"}}}]}`
			guarded, err := s.guardOfflineRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: config, OvRuntimeJson: `{"xray_policies":[{"user_id":1,"email":"quota-user","inbound_tag":"managed","status":"limited"}]}`})
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Inbounds []struct {
					Tag string `json:"tag"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal([]byte(guarded.ConfigJson), &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Tag != "static" {
				t.Fatalf("empty managed fallback survived or unmanaged listener changed: %s", guarded.ConfigJson)
			}
		})
	}
}
