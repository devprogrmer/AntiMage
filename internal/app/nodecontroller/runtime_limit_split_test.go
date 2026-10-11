package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestNativeRuntimePayloadSeparatesDeviceAndIPLimits(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "runtime-limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE users (
id INTEGER PRIMARY KEY, username TEXT, credential_key TEXT, status TEXT,
used_traffic INTEGER, data_limit INTEGER, expire INTEGER, device_limit INTEGER,
ip_limit INTEGER, upload_speed_limit INTEGER, download_speed_limit INTEGER, service_id INTEGER
);
CREATE TABLE wireguard_peer_addresses (
inbound_tag TEXT, user_id INTEGER, pool TEXT, server_address TEXT, address TEXT,
PRIMARY KEY (inbound_tag, user_id), UNIQUE (inbound_tag, address)
);
CREATE TABLE wireguard_devices (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,private_key TEXT NOT NULL,public_key TEXT NOT NULL,address TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,public_key),UNIQUE(inbound_tag,address));
CREATE TABLE amneziawg_devices (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,private_key TEXT NOT NULL,public_key TEXT NOT NULL,preshared_key TEXT NOT NULL DEFAULT '',address TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,address));
INSERT INTO users VALUES
(1, 'one-one', '0123456789abcdef0123456789abcdef', 'active', 0, NULL, NULL, 1, 1, 0, 0, 7),
(2, 'two-one', 'abcdef0123456789abcdef0123456789', 'active', 0, NULL, NULL, 2, 1, 0, 0, 7),
(3, 'one-two', 'fedcba9876543210fedcba9876543210', 'active', 0, NULL, NULL, 1, 2, 0, 0, 7);`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	checks := []struct {
		name string
		load func() (any, error)
	}{
		{"openvpn", func() (any, error) { return repo.OVUsersForServices(ctx, "ov", []int64{7}, "10.70.0.0/24") }},
		{"wireguard", func() (any, error) {
			return repo.WGUsersForServices(ctx, "wg", []int64{7}, "10.75.0.0/24", "10.75.0.1")
		}},
		{"amneziawg", func() (any, error) {
			return repo.AWGUsersForServices(ctx, "awg", []int64{7}, "10.76.0.0/24", "10.76.0.1", false)
		}},
		{"l2tp", func() (any, error) { return repo.L2TPUsersForServices(ctx, []int64{7}, "10.71.0.0/24") }},
		{"pptp", func() (any, error) { return repo.PPTPUsersForServices(ctx, []int64{7}, "10.72.0.0/24") }},
		{"ikev2", func() (any, error) { return repo.remoteAccessUsers(ctx, "ike", []int64{7}, "10.73.0.0/24", "ikev2") }},
		{"anyconnect", func() (any, error) {
			return repo.remoteAccessUsers(ctx, "ac", []int64{7}, "10.74.0.0/24", "anyconnect")
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			users, err := check.load()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(users)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []struct {
				UserID      int64 `json:"user_id"`
				DeviceLimit int64 `json:"device_limit"`
				IPLimit     int64 `json:"ip_limit"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			expectedCount := 3
			if check.name == "wireguard" || check.name == "amneziawg" {
				expectedCount = 4
			}
			if len(decoded) != expectedCount {
				t.Fatalf("users = %s", raw)
			}
			for i, user := range decoded {
				expected := map[int64][2]int64{1: {1, 1}, 2: {2, 1}, 3: {1, 2}}[user.UserID]
				if user.DeviceLimit != expected[0] || user.IPLimit != expected[1] {
					t.Fatalf("user %d limits conflated: %s", i, raw)
				}
			}
		})
	}
}
