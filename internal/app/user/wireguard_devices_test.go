package user

import (
	"context"
	"database/sql"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
)

func TestReconcileWireGuardDevicesUsesStableDistinctPeersAndRevokesOverflow(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "wg-devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE wireguard_devices (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,private_key TEXT NOT NULL,public_key TEXT NOT NULL,address TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,public_key),UNIQUE(inbound_tag,address))`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	credential := "0123456789abcdef0123456789abcdef"
	first, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 2, "10.70.0.0/24", "10.70.0.1/24", credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].PublicKey == first[1].PublicKey || first[0].Address == first[1].Address {
		t.Fatalf("invalid peers: %#v", first)
	}
	stable, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 2, "10.70.0.0/24", "10.70.0.1/24", credential)
	if err != nil {
		t.Fatal(err)
	}
	if stable[0] != first[0] || stable[1] != first[1] {
		t.Fatalf("identities changed: %#v %#v", first, stable)
	}
	trimmed, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 1, "10.70.0.0/24", "10.70.0.1/24", credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(trimmed) != 1 || trimmed[0].PublicKey != first[0].PublicKey {
		t.Fatalf("revoke failed: %#v", trimmed)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM wireguard_devices WHERE user_id=7`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d", count)
	}
}
