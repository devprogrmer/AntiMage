package nodeagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func nodeTLSCertificate(t *testing.T, name string, configure ...func(*x509.Certificate)) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	for _, configureCertificate := range configure {
		configureCertificate(cert)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kp := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	return pair, cp, kp
}

// net.Pipe exercises actual TLS verification without a host port or a retry
// timeout hiding why a certificate was refused.
func nodeTLSHandshake(t *testing.T, serverConfig *tls.Config, serverPEM []byte, clientIdentity tls.Certificate, version uint16) error {
	t.Helper()
	serverWire, clientWire := net.Pipe()
	defer serverWire.Close()
	defer clientWire.Close()
	deadline := time.Now().Add(2 * time.Second)
	_ = serverWire.SetDeadline(deadline)
	_ = clientWire.SetDeadline(deadline)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(serverPEM)
	clientConfig := &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: version, MaxVersion: version}
	if len(clientIdentity.Certificate) != 0 {
		clientConfig.Certificates = []tls.Certificate{clientIdentity}
	}
	serverDone, clientDone := make(chan error, 1), make(chan error, 1)
	go func() { serverDone <- tls.Server(serverWire, serverConfig).Handshake() }()
	go func() {
		client := tls.Client(clientWire, clientConfig)
		err := client.Handshake()
		if err == nil {
			var scratch [1]byte
			_, err = client.Read(scratch[:])
		}
		clientDone <- err
	}()
	serverErr := <-serverDone
	// A rejected TLS 1.3 client may have already finished its half of the
	// handshake. The server result is authoritative for control-plane access.
	serverWire.Close()
	clientWire.Close()
	<-clientDone
	return serverErr
}

func TestNodeControlTLSTrustRotationValidityAndPurpose(t *testing.T) {
	identity, serverPEM, _ := nodeTLSCertificate(t, "node-server")
	master, masterPEM, _ := nodeTLSCertificate(t, "master-old")
	next, nextPEM, _ := nodeTLSCertificate(t, "master-new")
	foreign, _, _ := nodeTLSCertificate(t, "master-old")
	expired, expiredPEM, _ := nodeTLSCertificate(t, "expired", func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-2 * time.Hour)
		c.NotAfter = time.Now().Add(-time.Hour)
	})
	future, futurePEM, _ := nodeTLSCertificate(t, "future", func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Minute) })
	wrongPurpose, wrongPEM, _ := nodeTLSCertificate(t, "server-only", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} })
	ca, caPEM, caKeyPEM := nodeTLSCertificate(t, "pinned-ca-identity", func(c *x509.Certificate) {
		c.IsCA, c.BasicConstraintsValid = true, true
		c.KeyUsage |= x509.KeyUsageCertSign
	})
	caCertificate, err := x509.ParseCertificate(ca.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	child, _, _ := nodeTLSCertificate(t, "signed-but-unpinned")
	childTemplate, err := x509.ParseCertificate(child.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	childDER, err := x509.CreateCertificate(rand.Reader, childTemplate, caCertificate, childTemplate.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	child.Certificate = [][]byte{childDER, ca.Certificate[0]}
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, tt := range []struct {
			name    string
			trust   []byte
			client  tls.Certificate
			allowed bool
		}{
			{"explicit separate master", masterPEM, master, true},
			{"node identity is not implicitly trusted with explicit pin", masterPEM, identity, false},
			{"same name different certificate", masterPEM, foreign, false},
			{"rotation keeps old pin", append(append([]byte{}, masterPEM...), nextPEM...), master, true},
			{"rotation accepts new pin", append(append([]byte{}, masterPEM...), nextPEM...), next, true},
			{"retired pin is rejected", nextPEM, master, false},
			{"expired pinned client", expiredPEM, expired, false},
			{"future pinned client", futurePEM, future, false},
			{"server-only pinned client", wrongPEM, wrongPurpose, false},
			{"pinned signer is not permission for other leaves", caPEM, child, false},
		} {
			t.Run(fmt.Sprintf("TLS%d/%s", version, tt.name), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "master.pem")
				if err := os.WriteFile(path, tt.trust, 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := nodeControlTLS(Config{MasterCertFile: path}, identity)
				if err != nil {
					t.Fatal(err)
				}
				err = nodeTLSHandshake(t, cfg, serverPEM, tt.client, version)
				if (err == nil) != tt.allowed {
					t.Fatalf("allowed=%v; handshake error=%v", tt.allowed, err)
				}
			})
		}
	}
}

func TestNodeControlTLSRejectsInvalidTrustFiles(t *testing.T) {
	identity, certificate, _ := nodeTLSCertificate(t, "node")
	for _, contents := range [][]byte{nil, []byte("not PEM"), []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----"), append(append([]byte{}, certificate...), []byte("trailing garbage")...), make([]byte, (1<<20)+1)} {
		path := filepath.Join(t.TempDir(), "trust.pem")
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := nodeControlTLS(Config{MasterCertFile: path}, identity); err == nil {
			t.Fatal("invalid explicit trust file was accepted")
		}
	}
	if _, err := nodeControlTLS(Config{MasterCertFile: filepath.Join(t.TempDir(), "missing")}, identity); err == nil {
		t.Fatal("missing explicit trust fell back to node identity")
	}
}

// The real listener must accept its install bundle and reject unrelated identities.
func TestNodeControlTLSAuthenticatesPinnedInstallBundle(t *testing.T) {
	t.Setenv("ANTIMAGE_NODE_QUOTA_ENFORCEMENT_INTERVAL", "off")
	trusted, cert, key := nodeTLSCertificate(t, "isolated-node")
	rogue, _, _ := nodeTLSCertificate(t, "unrelated-client-not-master")
	dir := t.TempDir()
	cp := filepath.Join(dir, "cert.pem")
	kp := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cp, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, key, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, portRaw, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portRaw)
	listener.Close()
	server := New(Config{Name: "tls-test-node", ListenHost: "127.0.0.1", ServicePort: port, DataDir: dir, CertFile: cp, KeyFile: kp, XrayPath: filepath.Join(dir, "not-installed"), Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("node shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("node did not stop")
		}
	})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cert)
	for _, tt := range []struct {
		name    string
		certs   []tls.Certificate
		allowed bool
	}{
		{"existing install bundle", []tls.Certificate{trusted}, true},
		{"foreign self-signed certificate", []tls.Certificate{rogue}, false},
		{"no client certificate", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requestCtx, requestCancel := context.WithTimeout(ctx, 3*time.Second)
			defer requestCancel()
			conn, err := grpc.DialContext(requestCtx, address, grpc.WithBlock(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "localhost", Certificates: tt.certs, MinVersion: tls.VersionTLS12})))
			if err != nil {
				if tt.allowed {
					t.Fatal(err)
				}
				return
			}
			defer conn.Close()
			response, err := nodev1.NewNodeControlServiceClient(conn).Hello(requestCtx, &nodev1.HelloRequest{})
			if tt.allowed {
				if err != nil {
					t.Fatal(err)
				}
				if response.GetNodeId() != "tls-test-node" {
					t.Fatalf("unexpected node: %v", response)
				}
			} else if err == nil {
				t.Fatal("unauthorized certificate reached node control RPC")
			}
		})
	}
}
