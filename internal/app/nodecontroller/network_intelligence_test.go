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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
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
			Resources *struct {
				Used      uint64 `json:"memory_used_bytes"`
				Total     uint64 `json:"memory_total_bytes"`
				SampledAt int64  `json:"sampled_at_unix"`
			} `json:"node_resources"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		if enabled && snapshot.Version != 1 || !enabled && (snapshot.Available == nil || *snapshot.Available) {
			t.Fatalf("enabled=%t snapshot=%s", enabled, raw)
		}
		if enabled && (snapshot.Resources == nil || snapshot.Resources.Total == 0 || snapshot.Resources.Used > snapshot.Resources.Total || snapshot.Resources.SampledAt <= 0) {
			t.Fatalf("missing or invalid node resource sample: %s", raw)
		}
		if !enabled && snapshot.Resources != nil {
			t.Fatal("disabled snapshot unexpectedly included resources")
		}
	}
}

func TestManageNetworkOptimizationFailsClosedForLegacyNodeRuntime(t *testing.T) {
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
	})))
	defer server.Stop()
	legacyAgent := nodeagent.New(nodeagent.Config{DataDir: t.TempDir()})
	nodev1.RegisterNodeControlServiceServer(server, legacyAgent)
	runtimeDesc := nodev1.NodeRuntimeService_ServiceDesc
	runtimeDesc.Methods = make([]grpc.MethodDesc, 0, len(nodev1.NodeRuntimeService_ServiceDesc.Methods))
	for _, method := range nodev1.NodeRuntimeService_ServiceDesc.Methods {
		if method.MethodName != "ManageNetworkOptimization" {
			runtimeDesc.Methods = append(runtimeDesc.Methods, method)
		}
	}
	server.RegisterService(&runtimeDesc, legacyAgent)
	go func() { _ = server.Serve(listener) }()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.Exec(`INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'legacy','127.0.0.1',?,?,'connected',?,?,'custom',1)`, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	c := NewController(NewRepository(db, "sqlite"))
	defer c.nodeClients.Range(func(_, value any) bool { _ = value.(cachedNodeClient).client.Close(); return true })
	_, err = c.ManageNetworkOptimization(ctx, &nodev1.NetworkOptimizationRequest{
		Action: nodev1.NetworkOptimizationRequest_APPLY_TCP_MSS, NodeId: 7, UserId: 42,
		OperationId: "1234567890abcdef1234567890abcdef", TcpMss: 1340,
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("legacy node must fail closed without mutating runtime; status=%s err=%v", status.Code(err), err)
	}
}
