package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "modernc.org/sqlite"
)

type wireGuardMutationRPCRecorder struct {
	nodev1.UnimplementedNodeRuntimeServiceServer
	removed chan *nodev1.RemoveInboundUserRequest
}

func (r *wireGuardMutationRPCRecorder) RemoveUser(_ context.Context, req *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.removed <- req
	return &nodev1.RuntimeActionResponse{Accepted: true}, nil
}

func TestControllerWireGuardRemoveUsesTargetedRPCAndStableIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "wg-targeted-remove.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, service_id INTEGER, status TEXT);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
INSERT INTO users VALUES (7, 'wg-user', 10, 'deleted');
INSERT INTO proxies VALUES (7, 'wireguard');
INSERT INTO hosts VALUES (1, 'wg-in', 0);
INSERT INTO service_hosts VALUES (10, 1);`); err != nil {
		t.Fatal(err)
	}
	target := "node:7"
	raw, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": "wg-in", "protocol": "wireguard", "targets": []any{target},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	tlsFixture := httptest.NewTLSServer(nil)
	certificate := tlsFixture.TLS.Certificates[0]
	tlsFixture.Close()
	rootCert, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	rootPool := x509.NewCertPool()
	rootPool.AddCert(rootCert)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	})))
	recorder := &wireGuardMutationRPCRecorder{removed: make(chan *nodev1.RemoveInboundUserRequest, 1)}
	nodev1.RegisterNodeRuntimeServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := nodeclient.Dial(callCtx, listener.Addr().String(), &tls.Config{RootCAs: rootPool, MinVersion: tls.VersionTLS12}, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetHandshake("test", []string{"targeted_user_add_remove_wireguard"})

	controller := NewController(NewRepository(db, "sqlite"))
	node := NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: raw}
	operation := OperationRow{ID: 91, OperationType: "remove_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}
	if err := controller.grpcApplyUserOperation(callCtx, client, node, operation, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-recorder.removed:
		if request.GetInboundTag() != "wg-in" || request.GetEmail() != "wireguard-user:7" || request.GetOperationId() != "remove_user-91-wg-in" {
			t.Fatalf("targeted remove request=%+v", request)
		}
	case <-callCtx.Done():
		t.Fatal("targeted WireGuard RemoveUser RPC was not sent")
	}
}

func TestControllerAmneziaWGRemoveUsesTargetedRPCAndStableIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "awg-targeted-remove.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, service_id INTEGER, status TEXT);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
INSERT INTO users VALUES (7, 'awg-user', 10, 'deleted');
INSERT INTO proxies VALUES (7, 'amneziawg');
INSERT INTO hosts VALUES (1, 'awg-in', 0);
INSERT INTO service_hosts VALUES (10, 1);`); err != nil {
		t.Fatal(err)
	}
	target := "node:7"
	raw, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": "awg-in", "protocol": "amneziawg", "targets": []any{target},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	tlsFixture := httptest.NewTLSServer(nil)
	certificate := tlsFixture.TLS.Certificates[0]
	tlsFixture.Close()
	rootCert, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	rootPool := x509.NewCertPool()
	rootPool.AddCert(rootCert)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	})))
	recorder := &wireGuardMutationRPCRecorder{removed: make(chan *nodev1.RemoveInboundUserRequest, 1)}
	nodev1.RegisterNodeRuntimeServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := nodeclient.Dial(callCtx, listener.Addr().String(), &tls.Config{RootCAs: rootPool, MinVersion: tls.VersionTLS12}, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetHandshake("test", []string{"targeted_user_add_remove_amneziawg"})
	controller := NewController(NewRepository(db, "sqlite"))
	node := NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: raw}
	operation := OperationRow{ID: 92, OperationType: "remove_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}
	if err := controller.grpcApplyUserOperation(callCtx, client, node, operation, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-recorder.removed:
		if request.GetInboundTag() != "awg-in" || request.GetEmail() != "amneziawg-user:7" || request.GetOperationId() != "remove_user-92-awg-in" {
			t.Fatalf("targeted AmneziaWG remove request=%+v", request)
		}
	case <-callCtx.Done():
		t.Fatal("targeted AmneziaWG RemoveUser RPC was not sent")
	}
}
