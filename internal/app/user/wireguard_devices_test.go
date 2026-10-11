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
	for _, statement := range []string{
		`CREATE TABLE wireguard_devices (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,private_key TEXT NOT NULL,public_key TEXT NOT NULL,address TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(inbound_tag,user_id,device_index),UNIQUE(inbound_tag,public_key),UNIQUE(inbound_tag,address))`,
		`CREATE TABLE wireguard_device_revocations (inbound_tag TEXT NOT NULL,user_id INTEGER NOT NULL,device_index INTEGER NOT NULL,public_key TEXT NOT NULL,credential_fingerprint TEXT NOT NULL,revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(inbound_tag,user_id,device_index))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
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
	if _, err := db.Exec(`INSERT INTO wireguard_device_revocations (inbound_tag,user_id,device_index,public_key,credential_fingerprint) VALUES ('wg-main',7,0,?,?)`, trimmed[0].PublicKey, WireGuardCredentialFingerprint(credential)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM wireguard_devices WHERE inbound_tag='wg-main' AND user_id=7 AND device_index=0`); err != nil {
		t.Fatal(err)
	}
	afterRevoke, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 1, "10.70.0.0/24", "10.70.0.1/24", credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRevoke) != 0 {
		t.Fatalf("revoked slot was silently recreated: %#v", afterRevoke)
	}
	spellingOnly, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 1, "10.70.0.0/24", "10.70.0.1/24", "01234567-89AB-CDEF-0123-456789ABCDEF")
	if err != nil || len(spellingOnly) != 0 {
		t.Fatalf("credential spelling change bypassed revocation: %#v err=%v", spellingOnly, err)
	}
	if _, err := db.Exec(`INSERT INTO wireguard_devices (inbound_tag,user_id,device_index,private_key,public_key,address) VALUES ('wg-main',7,1,?,?,?)`, first[1].PrivateKey, first[1].PublicKey, first[1].Address); err != nil {
		t.Fatal(err)
	}
	unlimited, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 0, "10.70.0.0/24", "10.70.0.1/24", credential)
	if err != nil || len(unlimited) != 1 || unlimited[0].PublicKey != first[1].PublicKey {
		t.Fatalf("unlimited reconciliation lost the non-revoked device: %#v err=%v", unlimited, err)
	}
	rotated, err := repo.ReconcileWireGuardDevices(context.Background(), "wg-main", 7, 1, "10.70.0.0/24", "10.70.0.1/24", "fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) != 1 || rotated[0].PublicKey == trimmed[0].PublicKey {
		t.Fatalf("credential rotation did not provision a fresh device: %#v", rotated)
	}
}
