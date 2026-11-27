package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	"github.com/antimage/antimage/internal/app/nodeagent"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestNetworkIntelligenceReadsRealNodeMetricsOverMTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	cert, key, err := nodeapp.GenerateCertificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cert)) {
		t.Fatal("invalid fixture certificate")
	}
	if _, err := db.Exec("INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	c := NewController(NewRepository(db, "sqlite"))
	defer c.nodeClients.Range(func(_, value any) bool { _ = value.(cachedNodeClient).client.Close(); return true })
	for index, enabled := range []bool{true, false} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair},
			ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
		})))
		agent := nodeagent.New(nodeagent.Config{NetworkIntelligenceEnabled: enabled, DataDir: t.TempDir()})
		nodev1.RegisterNodeControlServiceServer(server, agent)
		nodev1.RegisterNodeRuntimeServiceServer(server, agent)
		go func() { _ = server.Serve(listener) }()
		defer server.Stop()
		id, port := int64(index+1), listener.Addr().(*net.TCPAddr).Port
		if _, err := db.Exec(`INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (?,?,'127.0.0.1',?,?,'connected',?,?,'custom',1)`, id, listener.Addr().String(), port, port, cert, key); err != nil {
			t.Fatal(err)
		}
		raw, err := c.NetworkIntelligence(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			Version   int
			Available *bool
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		if enabled && snapshot.Version != 1 || !enabled && (snapshot.Available == nil || *snapshot.Available) {
			t.Fatalf("enabled=%t snapshot=%s", enabled, raw)
		}
	}
}
