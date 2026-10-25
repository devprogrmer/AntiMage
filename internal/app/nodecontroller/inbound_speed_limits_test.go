package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEffectiveSpeedLimitUsesStricterNonZeroValue(t *testing.T) {
	cases := []struct {
		name    string
		user    int64
		inbound int64
		want    int64
	}{
		{"both unlimited", 0, 0, 0},
		{"inbound only", 0, 8_000_000, 8_000_000},
		{"user only", 4_000_000, 0, 4_000_000},
		{"user below inbound cap", 4_000_000, 8_000_000, 4_000_000},
		{"user above inbound cap", 16_000_000, 8_000_000, 8_000_000},
		{"equal limits", 8_000_000, 8_000_000, 8_000_000},
		{"negative user treated as unlimited", -1, 8_000_000, 8_000_000},
		{"negative inbound treated as unlimited", 8_000_000, -1, 8_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveSpeedLimit(tc.user, tc.inbound); got != tc.want {
				t.Fatalf("effectiveSpeedLimit(%d, %d) = %d, want %d", tc.user, tc.inbound, got, tc.want)
			}
		})
	}
}

func TestRuntimeInboundSpeedLimitsReadsSanitizedPayload(t *testing.T) {
	// Sanitized payloads carry JSON-decoded numbers (float64); structured
	// columns carry int64. Both shapes must resolve to the same value.
	inbounds := []map[string]any{
		{"tag": "wg-in", "upload_speed_limit": float64(8_000_000), "download_speed_limit": float64(4_000_000)},
		{"tag": "int-in", "upload_speed_limit": int64(2_000_000), "download_speed_limit": int64(1_000_000)},
		{"tag": "no-cap"},
		{"tag": ""},
	}
	limits := runtimeInboundSpeedLimits(inbounds)
	if got := limits["wg-in"]; got.Upload != 8_000_000 || got.Download != 4_000_000 {
		t.Fatalf("wg-in limits = %+v", got)
	}
	if got := limits["int-in"]; got.Upload != 2_000_000 || got.Download != 1_000_000 {
		t.Fatalf("int-in limits = %+v", got)
	}
	if got := limits["no-cap"]; got.Upload != 0 || got.Download != 0 {
		t.Fatalf("no-cap limits = %+v", got)
	}
	if _, ok := limits[""]; ok {
		t.Fatalf("empty tag should be skipped")
	}
}

// inboundSpeedCapSchema is the minimal inbound/user schema shared by the
// speed-cap tests. Users deliberately cover the three interesting cases:
// above the cap, unlimited, and below it.
const inboundSpeedCapSchema = `
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
CREATE TABLE wireguard_device_revocations (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,credential_fingerprint TEXT NOT NULL,revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(inbound_tag,user_id,device_index));
CREATE TABLE amneziawg_devices (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,private_key TEXT NOT NULL,public_key TEXT NOT NULL,preshared_key TEXT NOT NULL DEFAULT '',address TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,address));
INSERT INTO users VALUES
(1, 'capped-down', '0123456789abcdef0123456789abcdef', 'active', 0, NULL, NULL, 1, 1, 16000000, 16000000, 7),
(2, 'capped-inherit', 'abcdef0123456789abcdef0123456789', 'active', 0, NULL, NULL, 1, 1, 0, 0, 7),
(3, 'keeps-own', 'fedcba9876543210fedcba9876543210', 'active', 0, NULL, NULL, 1, 1, 2000000, 1000000, 7);
`

// inboundSpeedCapWant is the resolved pair each user must end up with when the
// inbound enforces 8 Mbps up and 8 Mbps down.
var inboundSpeedCapWant = map[int64][2]int64{
	1: {8_000_000, 8_000_000}, // above the cap -> clamped down to the cap
	2: {8_000_000, 8_000_000}, // unlimited -> inherits the cap
	3: {2_000_000, 1_000_000}, // below the cap -> keeps its own stricter limit
}

// TestInboundSpeedCapPropagatesThroughRuntimeBuilders drives the real
// per-protocol user loaders, then resolves the cap through the production
// helper exactly as each runtime builder does. It proves the stricter non-zero
// value wins and that a user-level limit can never exceed the enforced cap.
func TestInboundSpeedCapPropagatesThroughRuntimeBuilders(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "inbound-speed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(inboundSpeedCapSchema); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	limit := inboundSpeedLimit{Upload: 8_000_000, Download: 8_000_000}

	t.Run("openvpn", func(t *testing.T) {
		users, err := repo.OVUsersForServices(ctx, "ov", []int64{7}, "10.70.0.0/24")
		if err != nil {
			t.Fatal(err)
		}
		applyInboundSpeedLimitToUsers(limit, users, func(u *OVRuntimeUser) *int64 { return &u.UploadSpeedLimit }, func(u *OVRuntimeUser) *int64 { return &u.DownloadSpeedLimit })
		for _, u := range users {
			assertUserSpeed(t, u.UserID, u.UploadSpeedLimit, u.DownloadSpeedLimit)
		}
	})

	t.Run("wireguard", func(t *testing.T) {
		peers, err := repo.WGUsersForServices(ctx, "wg", []int64{7}, "10.75.0.0/24", "10.75.0.1")
		if err != nil {
			t.Fatal(err)
		}
		applyInboundSpeedLimitToUsers(limit, peers, func(p *WGRuntimePeer) *int64 { return &p.UploadSpeedLimit }, func(p *WGRuntimePeer) *int64 { return &p.DownloadSpeedLimit })
		for _, p := range peers {
			assertUserSpeed(t, p.UserID, p.UploadSpeedLimit, p.DownloadSpeedLimit)
		}
	})

	t.Run("amneziawg", func(t *testing.T) {
		peers, err := repo.AWGUsersForServices(ctx, "awg", []int64{7}, "10.76.0.0/24", "10.76.0.1", false)
		if err != nil {
			t.Fatal(err)
		}
		applyInboundSpeedLimitToUsers(limit, peers, func(p *AWGRuntimePeer) *int64 { return &p.UploadSpeedLimit }, func(p *AWGRuntimePeer) *int64 { return &p.DownloadSpeedLimit })
		for _, p := range peers {
			assertUserSpeed(t, p.UserID, p.UploadSpeedLimit, p.DownloadSpeedLimit)
		}
	})

	t.Run("l2tp", func(t *testing.T) {
		users, err := repo.L2TPUsersForServices(ctx, []int64{7}, "10.71.0.0/24")
		if err != nil {
			t.Fatal(err)
		}
		applyInboundSpeedLimitToUsers(limit, users, func(u *L2TPRuntimeUser) *int64 { return &u.UploadSpeedLimit }, func(u *L2TPRuntimeUser) *int64 { return &u.DownloadSpeedLimit })
		for _, u := range users {
			assertUserSpeed(t, u.UserID, u.UploadSpeedLimit, u.DownloadSpeedLimit)
		}
	})

	t.Run("pptp", func(t *testing.T) {
		users, err := repo.PPTPUsersForServices(ctx, []int64{7}, "10.72.0.0/24")
		if err != nil {
			t.Fatal(err)
		}
		applyInboundSpeedLimitToUsers(limit, users, func(u *PPTPRuntimeUser) *int64 { return &u.UploadSpeedLimit }, func(u *PPTPRuntimeUser) *int64 { return &u.DownloadSpeedLimit })
		for _, u := range users {
			assertUserSpeed(t, u.UserID, u.UploadSpeedLimit, u.DownloadSpeedLimit)
		}
	})

	for _, protocol := range []string{"ikev2", "anyconnect"} {
		t.Run(protocol, func(t *testing.T) {
			users, err := repo.remoteAccessUsers(ctx, protocol, []int64{7}, "10.73.0.0/24", protocol)
			if err != nil {
				t.Fatal(err)
			}
			applyInboundSpeedLimitToUsers(limit, users, func(u *RemoteAccessRuntimeUser) *int64 { return &u.UploadSpeedLimit }, func(u *RemoteAccessRuntimeUser) *int64 { return &u.DownloadSpeedLimit })
			for _, u := range users {
				assertUserSpeed(t, u.UserID, u.UploadSpeedLimit, u.DownloadSpeedLimit)
			}
		})
	}
}

func assertUserSpeed(t *testing.T, userID, upload, download int64) {
	t.Helper()
	expected, ok := inboundSpeedCapWant[userID]
	if !ok {
		t.Fatalf("unexpected user %d", userID)
	}
	if upload != expected[0] || download != expected[1] {
		t.Fatalf("user %d speed = %d/%d, want %d/%d", userID, upload, download, expected[0], expected[1])
	}
}

// TestInboundSpeedLimitsReadFromStructuredColumns covers the targeted mutation
// path: the raw Xray config never carries the cap keys, so the controller reads
// them from the inbounds table instead.
func TestInboundSpeedLimitsReadFromStructuredColumns(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "inbound-speed-columns.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER, downlink INTEGER, usage_coefficient REAL, upload_speed_limit INTEGER, download_speed_limit INTEGER);
INSERT INTO inbounds (tag, uplink, downlink, usage_coefficient, upload_speed_limit, download_speed_limit) VALUES
('capped-in', 0, 0, 1.0, 8000000, 4000000),
('unlimited-in', 0, 0, 1.0, 0, 0);`); err != nil {
		t.Fatal(err)
	}
	limits, err := NewRepository(db, "sqlite").inboundSpeedLimitsByTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := limits["capped-in"]; got.Upload != 8_000_000 || got.Download != 4_000_000 {
		t.Fatalf("capped-in = %+v", got)
	}
	if got := limits["unlimited-in"]; got.Upload != 0 || got.Download != 0 {
		t.Fatalf("unlimited-in = %+v", got)
	}
}

// TestInboundSpeedLimitsToleratePreMigrationSchema guards the upgrade path: an
// old database without the new columns must degrade to "no cap" rather than
// failing the mutation.
func TestInboundSpeedLimitsToleratePreMigrationSchema(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "legacy-inbounds.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER, downlink INTEGER, usage_coefficient REAL);
INSERT INTO inbounds VALUES ('legacy', 0, 0, 1.0);`); err != nil {
		t.Fatal(err)
	}
	limits, err := NewRepository(db, "sqlite").inboundSpeedLimitsByTag(ctx)
	if err != nil {
		t.Fatalf("pre-migration schema should not fail: %v", err)
	}
	if len(limits) != 0 {
		t.Fatalf("expected empty limits, got %+v", limits)
	}
}
