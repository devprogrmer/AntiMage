package nodecontroller

import (
	"context"
	"database/sql"
	"testing"
)

func TestIncludeDBUsersDisablesEmptyFallbackListeners(t *testing.T) {
	for _, protocol := range []string{"shadowsocks", "hysteria"} {
		t.Run(protocol, func(t *testing.T) {
			for _, hasUser := range []bool{false, true} {
				raw := map[string]any{"inbounds": []any{
					map[string]any{"tag": "API", "protocol": "tunnel"},
					map[string]any{"tag": "managed", "protocol": protocol, "settings": map[string]any{"method": "2022-blake3-aes-128-gcm", "password": "AAAAAAAAAAAAAAAAAAAAAA==", "clients": []any{}}},
					map[string]any{"tag": "vless", "protocol": "vless", "settings": map[string]any{"clients": []any{}}},
				}}
				data := &runtimeConfigData{serviceTags: map[int64]map[string]bool{7: {"managed": true}}, masks: map[string][]byte{}}
				if hasUser {
					data.users = []runtimeUserRow{{ID: 1, Username: "alice", CredentialKey: "05bfddf81eb418fa1edbce7cd286eee1", Protocol: protocol, ServiceID: sql.NullInt64{Int64: 7, Valid: true}, Settings: map[string]any{}}}
				}
				if err := (Controller{}).includeDBUsers(context.Background(), raw, data); err != nil {
					t.Fatal(err)
				}
				inbounds := listOfMaps(raw["inbounds"])
				if !hasUser {
					if len(inbounds) != 2 || stringValue(inbounds[0]["tag"]) != "API" || stringValue(inbounds[1]["tag"]) != "vless" {
						t.Fatalf("empty managed listener exposes fallback auth: %#v", inbounds)
					}
				} else if len(inbounds) != 3 || len(interfaceSlice(mapValue(inbounds[1]["settings"])["clients"])) != 1 {
					t.Fatalf("active managed listener lost: %#v", inbounds)
				}
			}
		})
	}
}
