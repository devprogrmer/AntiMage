package nodecontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	added   chan *nodev1.InboundUserRequest
	updated chan *nodev1.InboundUserRequest
}

func (r *wireGuardMutationRPCRecorder) RemoveUser(_ context.Context, req *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.removed <- req
	return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: req.GetOperationId()}, nil
}

func TestWireGuardTargetedCapabilityRequiresAdvertisedAssignedSingleProtocol(t *testing.T) {
	client := &nodeclient.Client{}
	client.SetHandshake("test", []string{"targeted_user_add_remove_wireguard", "targeted_user_update_wireguard"})
	for _, test := range []struct {
		name      string
		protocols []string
		operation string
		assigned  bool
		want      bool
	}{
		{name: "capable assigned add", protocols: []string{"wireguard"}, operation: "add_user", assigned: true, want: true},
		{name: "legacy node", protocols: []string{"wireguard"}, operation: "add_user", assigned: false},
		{name: "mixed protocol remains conservative", protocols: []string{"wireguard", "vless"}, operation: "add_user", assigned: true},
		{name: "unadvertised operation", protocols: []string{"wireguard"}, operation: "sync_config", assigned: true},
		{name: "targeted update", protocols: []string{"wireguard"}, operation: "update_user", assigned: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := supportsTargetedUserMutation(client, test.protocols, test.operation, test.assigned); got != test.want {
				t.Fatalf("targeted=%v want=%v", got, test.want)
			}
		})
	}
}

func (r *wireGuardMutationRPCRecorder) AddUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.added <- req
	return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: req.GetOperationId()}, nil
}

func (r *wireGuardMutationRPCRecorder) UpdateUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.updated <- req
	return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: req.GetOperationId()}, nil
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

func TestControllerWireGuardAddAndUpdateUseTargetedRPC(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "wg-targeted-add-update.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, service_id INTEGER, status TEXT, credential_key TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 0, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER DEFAULT 0, downlink INTEGER DEFAULT 0, usage_coefficient REAL DEFAULT 1);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT, address TEXT, port INTEGER, api_port INTEGER, status TEXT, xray_version TEXT, message TEXT, certificate TEXT, certificate_key TEXT, xray_config_mode TEXT, xray_config TEXT, usage_coefficient REAL DEFAULT 1);
CREATE TABLE wireguard_devices (inbound_tag TEXT, user_id INTEGER, device_index INTEGER, private_key TEXT, public_key TEXT, address TEXT, generation INTEGER DEFAULT 1, PRIMARY KEY(inbound_tag,user_id,device_index));
CREATE TABLE wireguard_device_revocations (inbound_tag TEXT, user_id INTEGER, device_index INTEGER, credential_fingerprint TEXT);
CREATE TABLE node_wireguard_usage_reflection (node_id INTEGER, user_id INTEGER, batch_id TEXT);
INSERT INTO users VALUES (7,'wg-user',10,'active','0123456789abcdef0123456789abcdef',0,0,0,1,0,0,0);
INSERT INTO proxies VALUES (7,'wireguard');
INSERT INTO hosts VALUES (1,'wg-in',0);
INSERT INTO service_hosts VALUES (10,1);
INSERT INTO xray_config VALUES (1,'{"inbounds":[{"tag":"wg-in","protocol":"wireguard","port":51820,"targets":["node:7"],"settings":{"private_key":"DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=","address_pool":"10.69.0.0/24","server_address":"10.69.0.1"}}]}');
INSERT INTO inbounds VALUES ('wg-in',0,0,1);
INSERT INTO nodes VALUES (7,'wg-node','127.0.0.1',0,0,'connected','','','certificate','key','custom','{}',1);
INSERT INTO wireguard_devices VALUES ('wg-in',7,0,'private','BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=','10.69.0.7',1);`); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": "wg-in", "protocol": "wireguard", "port": 51820, "targets": []any{"node:7"},
		"settings": map[string]any{"private_key": strings.Repeat("D", 43) + "=", "address_pool": "10.69.0.0/24", "server_address": "10.69.0.1"},
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
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})))
	recorder := &wireGuardMutationRPCRecorder{removed: make(chan *nodev1.RemoveInboundUserRequest, 1), added: make(chan *nodev1.InboundUserRequest, 1), updated: make(chan *nodev1.InboundUserRequest, 1)}
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
	client.SetHandshake("test", []string{"targeted_user_add_remove_wireguard", "targeted_user_update_wireguard"})
	controller := NewController(NewRepository(db, "sqlite"))
	node := NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: raw}
	for _, operation := range []OperationRow{
		{ID: 92, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}},
		{ID: 93, OperationType: "update_user", UserID: sql.NullInt64{Int64: 7, Valid: true}},
	} {
		if err := controller.grpcApplyUserOperation(callCtx, client, node, operation, nil); err != nil {
			t.Fatalf("%s targeted RPC: %v", operation.OperationType, err)
		}
		var request *nodev1.InboundUserRequest
		channel := recorder.updated
		if operation.OperationType == "add_user" {
			channel = recorder.added
		}
		select {
		case request = <-channel:
		case <-callCtx.Done():
			t.Fatalf("%s targeted RPC was not sent", operation.OperationType)
		}
		if request.GetInboundTag() != "wg-in" || request.GetOperationId() != operation.OperationType+"-"+fmt.Sprint(operation.ID)+"-wg-in" || request.GetUser().GetProtocol() != "wireguard" {
			t.Fatalf("%s RPC envelope=%+v", operation.OperationType, request)
		}
	}
}
