package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/migrations"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	"github.com/antimage/antimage/internal/app/nodecontroller"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type historyTransportAgent struct {
	nodev1.UnimplementedNodeControlServiceServer
	nodev1.UnimplementedNodeRuntimeServiceServer
	snapshot string
}

func (*historyTransportAgent) Hello(context.Context, *nodev1.HelloRequest) (*nodev1.HelloResponse, error) {
	return &nodev1.HelloResponse{}, nil
}

func (a *historyTransportAgent) Metrics(_ context.Context, request *nodev1.MetricsRequest) (*nodev1.MetricsResponse, error) {
	if !request.GetIncludeNetworkIntelligence() {
		return &nodev1.MetricsResponse{}, nil
	}
	return &nodev1.MetricsResponse{NetworkIntelligenceJson: a.snapshot}, nil
}

func TestNetworkHistoryWorkerPersistsThroughMTLSAndDatabaseReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := now.Add(-time.Second)
	rtt := 42.5
	snapshot, err := json.Marshal(map[string]any{"history": map[string]any{
		"points": []persistedNetworkPoint{{NodeID: 7, UserID: 42, At: at, RTT: &rtt}},
	}})
	if err != nil {
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
	})))
	defer rpc.Stop()
	agent := &historyTransportAgent{snapshot: string(snapshot)}
	nodev1.RegisterNodeControlServiceServer(rpc, agent)
	nodev1.RegisterNodeRuntimeServiceServer(rpc, agent)
	go func() { _ = rpc.Serve(listener) }()

	path := filepath.Join(t.TempDir(), "panel.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'history-fixture','127.0.0.1',?,?,'connected',?,?,'custom',1)`, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	controller := nodecontroller.NewController(nodecontroller.NewRepository(db, "sqlite"))
	if _, err := controller.NetworkIntelligence(ctx, 7); err != nil {
		t.Fatalf("fixture node is unreachable: %v", err)
	}
	ticks := make(chan time.Time, 2)
	ticks <- now
	ticks <- now
	close(ticks)
	runNetworkHistoryCollector(ctx, ticks, controller, &ni.HistoryRepository{DB: db}, func() time.Time { return now })
	rpc.Stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s := &Server{db: reopened, cfg: Config{NetworkIntelligenceEnabled: true, NetworkHistoryPersistence: true}}
	req := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/history?node_id=7&user_id=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), adminContextKey, adminPrincipal{Role: "full_access"}))
	res := httptest.NewRecorder()
	s.handleNetworkIntelligenceHistory(res, req)
	var history networkHistoryResponse
	if err := json.Unmarshal(res.Body.Bytes(), &history); err != nil || res.Code != http.StatusOK || history.Storage != "durable" || len(history.Points) != 1 {
		t.Fatalf("history after database reopen: status=%d body=%s error=%v", res.Code, res.Body, err)
	}
	var point persistedNetworkPoint
	if err := json.Unmarshal(history.Points[0], &point); err != nil || point.NodeID != 7 || point.UserID != 42 || !point.At.Equal(at) || point.RTT == nil || *point.RTT != rtt {
		t.Fatalf("wrong persisted point: %s, error=%v", history.Points[0], err)
	}
}
