package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/antimage/antimage/internal/app/migrations"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type durableUserSyncFixture struct {
	nodev1.UnimplementedNodeControlServiceServer
	nodev1.UnimplementedNodeRuntimeServiceServer
	mu    sync.Mutex
	syncs int
}

func (f *durableUserSyncFixture) Hello(context.Context, *nodev1.HelloRequest) (*nodev1.HelloResponse, error) {
	return &nodev1.HelloResponse{NodeVersion: "test", Runtime: &nodev1.RuntimeState{Connected: true, Capabilities: []string{"config_revision", "full_config_sync", "durable_user_config_sync"}}}, nil
}
func (f *durableUserSyncFixture) SyncConfig(_ context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs++
	return &nodev1.RuntimeActionResponse{Accepted: true, Runtime: &nodev1.RuntimeState{Connected: true, Started: true, AppliedRevision: req.DesiredRevision}}, nil
}

func TestUserOperationsUseDurableSyncCapability(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})))
	fixture := &durableUserSyncFixture{}
	nodev1.RegisterNodeControlServiceServer(server, fixture)
	nodev1.RegisterNodeRuntimeServiceServer(server, fixture)
	go server.Serve(listener)
	defer server.Stop()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.Exec(`INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'fixture','127.0.0.1',?,?,'connected',?,?,'custom',1)`, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	c := NewController(NewRepository(db, "sqlite"))
	defer c.nodeClients.Range(func(_, value any) bool { value.(cachedNodeClient).client.Close(); return true })
	// A previously prepared targeted decision must be upgraded when the node
	// advertises durable policy sync. No legacy user RPC may acknowledge it.
	prepared := &preparedRuntimeConfig{nodeID: 7, userSyncDecided: true, userSyncRequired: false, configJSON: `{"inbounds":[]}`, ovRuntimeJSON: `{"xray_policies":[]}`}
	for i, operation := range []string{"add_user", "update_user", "remove_user"} {
		if err := c.applyOperation(ctx, OperationRow{ID: int64(i + 1), OperationType: operation, NodeID: sql.NullInt64{Int64: 7, Valid: true}, UserID: sql.NullInt64{Int64: 42, Valid: true}}, prepared); err != nil {
			t.Fatal(err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.syncs != 3 {
		t.Fatalf("durable sync calls=%d, want 3", fixture.syncs)
	}
}
