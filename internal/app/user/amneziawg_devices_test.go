package user

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestReconcileAmneziaWGDevicesUsesDistinctStableKeysAndAddresses(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "awg-devices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE amneziawg_devices (
		inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL,
		private_key TEXT NOT NULL, public_key TEXT NOT NULL, preshared_key TEXT NOT NULL DEFAULT '',
		address TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (inbound_tag, user_id, device_index))`); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db, "sqlite")
	first, err := repo.ReconcileAmneziaWGDevices(context.Background(), "awg-main", 7, 2, "10.72.0.0/24", "10.72.0.1/24", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("devices=%d want 2", len(first))
	}
	if first[0].PublicKey == first[1].PublicKey || first[0].PrivateKey == first[1].PrivateKey || first[0].Address == first[1].Address {
		t.Fatalf("device identities are not distinct: %#v", first)
	}
	if first[0].PresharedKey == "" || first[1].PresharedKey == "" {
		t.Fatalf("missing PSK: %#v", first)
	}
	second, err := repo.ReconcileAmneziaWGDevices(context.Background(), "awg-main", 7, 2, "10.72.0.0/24", "10.72.0.1/24", true)
	if err != nil {
		t.Fatal(err)
	}
	if first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("unrelated reconcile changed keys: first=%#v second=%#v", first, second)
	}
}

func TestReconcileAmneziaWGDevicesRevokesTrimmedSlots(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "awg-trim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE amneziawg_devices (
		inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL,
		private_key TEXT NOT NULL, public_key TEXT NOT NULL, preshared_key TEXT NOT NULL DEFAULT '',
		address TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (inbound_tag, user_id, device_index))`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	before, err := repo.ReconcileAmneziaWGDevices(context.Background(), "awg-main", 9, 3, "10.72.0.0/24", "10.72.0.1/24", false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := repo.ReconcileAmneziaWGDevices(context.Background(), "awg-main", 9, 1, "10.72.0.0/24", "10.72.0.1/24", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("devices=%d want 1", len(after))
	}
	if before[0].PublicKey != after[0].PublicKey {
		t.Fatal("device zero changed while lowering limit")
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM amneziawg_devices WHERE inbound_tag='awg-main' AND user_id=9`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("persisted devices=%d want 1", rows)
	}
}
