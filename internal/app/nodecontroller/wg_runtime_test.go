package nodecontroller

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOVServiceIDsForInboundIgnoresDisabledHosts(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "wg-active-hosts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
INSERT INTO hosts (id, inbound_tag, is_disabled) VALUES (1, 'wg-main', 1), (2, 'wg-main', 0);
INSERT INTO service_hosts (service_id, host_id) VALUES (10, 1), (20, 2);`); err != nil {
		t.Fatal(err)
	}

	got, err := NewRepository(db, "sqlite").OVServiceIDsForInbound(context.Background(), "wg-main")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{20}; !reflect.DeepEqual(got, want) {
		t.Fatalf("service IDs = %v, want %v", got, want)
	}
}

func TestAttachWGUsageReflectionsToPeers(t *testing.T) {
	ctx := context.Background()

	db, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(t.TempDir(), "wg-reflected-runtime.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `
CREATE TABLE node_wireguard_usage_reflection (
node_id INTEGER NOT NULL,
user_id INTEGER NOT NULL,
batch_id TEXT NOT NULL,
updated_at DATETIME NOT NULL,
PRIMARY KEY (node_id, user_id)
);

INSERT INTO node_wireguard_usage_reflection (
node_id,
user_id,
batch_id,
updated_at
) VALUES
(7, 10, 'wireguard-reflected-100', CURRENT_TIMESTAMP),
(8, 10, 'wireguard-wrong-node', CURRENT_TIMESTAMP);
`)
	if err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db, "sqlite")

	peers := []WGRuntimePeer{
		{
			UserID:   10,
			Username: "alice",
		},
		{
			UserID:   20,
			Username: "bob",
		},
	}

	if err := repo.attachWGUsageReflections(
		ctx,
		7,
		peers,
	); err != nil {
		t.Fatal(err)
	}

	if peers[0].ReflectedUsageBatchID != "wireguard-reflected-100" {
		t.Fatalf(
			"user 10 reflected batch = %q, want %q",
			peers[0].ReflectedUsageBatchID,
			"wireguard-reflected-100",
		)
	}

	if peers[1].ReflectedUsageBatchID != "" {
		t.Fatalf(
			"user 20 reflected batch = %q, want empty",
			peers[1].ReflectedUsageBatchID,
		)
	}
}

func TestProtocolRuntimeKeepsAmneziaWGIndependent(t *testing.T) {
	if reflect.TypeOf(AWGRuntime{}) == reflect.TypeOf(WGRuntime{}) {
		t.Fatal("AmneziaWG runtime must have an independent type")
	}
}

func TestAWGUsersForServicesClosesUserRowsBeforeReconcilingDevices(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "awg-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE users (
 id INTEGER PRIMARY KEY, username TEXT, status TEXT, used_traffic INTEGER,
 data_limit INTEGER, expire INTEGER, device_limit INTEGER,
 upload_speed_limit INTEGER, download_speed_limit INTEGER, service_id INTEGER
);
CREATE TABLE amneziawg_devices (
 inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL,
 private_key TEXT NOT NULL, public_key TEXT NOT NULL, preshared_key TEXT NOT NULL DEFAULT '',
 address TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY (inbound_tag, user_id, device_index),
 UNIQUE (inbound_tag, address)
);
INSERT INTO users (id, username, status, used_traffic, device_limit, upload_speed_limit, download_speed_limit, service_id)
VALUES (282, 'awg-user', 'active', 0, 1, 0, 0, 7);`); err != nil {
		t.Fatal(err)
	}

	peers, err := NewRepository(db, "sqlite").AWGUsersForServices(
		context.Background(), "awg-main", []int64{7}, "10.68.0.0/24", "10.68.0.1/24", false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].UserID != 282 || peers[0].Address != "10.68.0.2" {
		t.Fatalf("unexpected peers: %#v", peers)
	}
}
