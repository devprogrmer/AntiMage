package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type legacyWireAgent struct {
	nodev1.UnimplementedNodeControlServiceServer
}

func (legacyWireAgent) Hello(context.Context, *nodev1.HelloRequest) (*nodev1.HelloResponse, error) {
	return &nodev1.HelloResponse{Runtime: &nodev1.RuntimeState{Connected: true, Started: true}}, nil
}

func (legacyWireAgent) Connect(context.Context, *nodev1.ConnectRequest) (*nodev1.ConnectResponse, error) {
	return &nodev1.ConnectResponse{Runtime: &nodev1.RuntimeState{Connected: true, Started: true}}, nil
}

func TestLegacyAgentTLSConnectionRemainsVisibleWithoutDestructiveDispatch(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrations.RunMigrations(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	c := NewController(NewRepository(db, "sqlite"))
	tlsFixture := httptest.NewTLSServer(nil)
	certificate := tlsFixture.TLS.Certificates[0]
	tlsFixture.Close()
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}))
	keyBytes, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var destructive atomic.Int32
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if strings.Contains(info.FullMethod, "/NodeRuntimeService/") {
			destructive.Add(1)
		}
		return handler(ctx, req)
	}))
	nodev1.RegisterNodeControlServiceServer(server, legacyWireAgent{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := c.repo.db.Exec(`INSERT INTO nodes(id,name,address,port,api_port,status,certificate,certificate_key,node_capabilities) VALUES (1,'legacy-node','127.0.0.1',?,?,'connecting',?,?,'[]')`, port, port, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := c.repo.db.Exec("INSERT INTO tls(certificate,`key`) VALUES (?,?)", certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := c.Connect(ctx, Request{NodeID: 1})
	if err != nil || result.Status != "connected" || !result.Connected || result.NodeServiceVersion != "" || !strings.Contains(result.Message, "upgrade required") {
		t.Fatalf("legacy connection lost or fabricated identity: %+v %v", result, err)
	}
	if err := c.StopNodeRuntime(ctx, 1); err == nil || !isPermanentOperationError(err) {
		t.Fatalf("legacy destructive policy did not reject permanently: %v", err)
	}
	var status, caps string
	if err := c.repo.db.QueryRow(`SELECT status,node_capabilities FROM nodes WHERE id=1`).Scan(&status, &caps); err != nil || status != "connected" || caps != "null" && caps != "[]" {
		t.Fatalf("legacy visibility/capabilities: %s %s %v", status, caps, err)
	}
	var queued int
	if err := c.repo.db.QueryRow(`SELECT COUNT(*) FROM node_operations WHERE node_id=1`).Scan(&queued); err != nil || queued != 0 || destructive.Load() != 0 {
		t.Fatalf("legacy connection mutated runtime: queue=%d RPCs=%d err=%v", queued, destructive.Load(), err)
	}
}

func (legacyWireAgent) Health(context.Context, *nodev1.HealthRequest) (*nodev1.HealthResponse, error) {
	// Legacy wire messages omit every new capability and runtime identity field.
	return &nodev1.HealthResponse{Runtime: &nodev1.RuntimeState{Connected: true, Started: true}}, nil
}

func TestLegacyAgentWireHealthKeepsUnknownEvidenceUnknown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	nodev1.RegisterNodeControlServiceServer(server, legacyWireAgent{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := nodev1.NewNodeControlServiceClient(connection).Health(ctx, &nodev1.HealthRequest{OperationId: "new-controller-request"})
	if err != nil {
		t.Fatal(err)
	}
	state := response.GetRuntime()
	profile := ClassifyDestructiveCapabilities(state.GetCapabilities())
	if !state.GetConnected() || !state.GetStarted() || state.GetNodeVersion() != "" || state.GetProcessStartedAtUnixNano() != 0 || state.GetActiveConfigSha256() != "" || profile.SupportsFencing || profile.SupportsCommandIdempotency || profile.SupportsGeoIdentity {
		t.Fatalf("legacy wire response fabricated evidence: %+v %+v", state, profile)
	}
}
