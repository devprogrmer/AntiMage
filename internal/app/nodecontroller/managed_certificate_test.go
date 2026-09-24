package nodecontroller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	certificateapp "github.com/antimage/antimage/internal/app/certificates"
	_ "modernc.org/sqlite"
)

func TestManagedCertificateResolution(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "certs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE subscription_domains (
		id INTEGER PRIMARY KEY, domain TEXT UNIQUE, admin_id INTEGER, email TEXT,
		provider TEXT, alt_names TEXT, last_issued_at TEXT, last_renewed_at TEXT
	); INSERT INTO subscription_domains (domain) VALUES ('vpn.example.com')`)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(certificateapp.ManagedBaseDir(root), "vpn.example.com")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vpn.example.com"},
		DNSNames: []string{"vpn.example.com"}, NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(48 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vpn.example.com"},
		DNSNames: []string{"vpn.example.com"}, NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(48 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite", root)
	loadedCert, loadedKey, err := repo.loadManagedHAProxyCertificate(ctx, "vpn.example.com", "vpn.example.com")
	if err != nil || string(loadedCert) != string(certPEM) || string(loadedKey) != string(keyPEM) {
		t.Fatalf("managed pair was not loaded: %v", err)
	}
	if _, _, err := repo.loadManagedHAProxyCertificate(ctx, "vpn.example.com", "other.example.com"); err == nil {
		t.Fatal("hostname mismatch was accepted")
	}
	controller := NewController(repo)
	raw := map[string]any{"inbounds": []any{map[string]any{
		"tag": "tls", "streamSettings": map[string]any{"tlsSettings": map[string]any{
			"certificates": []any{map[string]any{"managedDomain": "vpn.example.com"}},
		}},
	}}}
	if err := controller.resolveManagedTLSCertificates(ctx, raw); err != nil {
		t.Fatal(err)
	}
	resolved := firstRuntimeCertificate(t, raw)
	if _, present := resolved["managedDomain"]; present {
		t.Fatal("managed reference leaked into node configuration")
	}
	if len(resolved["key"].([]string)) == 0 {
		t.Fatal("runtime key was not injected")
	}
	if err := os.WriteFile(filepath.Join(dir, ".metadata"), []byte("status=revoked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.loadManagedHAProxyCertificate(ctx, "vpn.example.com", "vpn.example.com"); err == nil {
		t.Fatal("revoked certificate was accepted")
	}
}
