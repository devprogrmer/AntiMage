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
