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

func TestControllerAmneziaWGOperationsUseAdvertisedTargetedRPC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "awg-targeted.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, service_id INTEGER, status TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 1, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER DEFAULT 0, downlink INTEGER DEFAULT 0, usage_coefficient REAL DEFAULT 1, upload_speed_limit INTEGER NOT NULL DEFAULT 0, download_speed_limit INTEGER NOT NULL DEFAULT 0);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT, address TEXT, port INTEGER, api_port INTEGER, status TEXT, xray_version TEXT, message TEXT, certificate TEXT, certificate_key TEXT, xray_config_mode TEXT, xray_config TEXT, usage_coefficient REAL DEFAULT 1);
CREATE TABLE amneziawg_devices (inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, device_index INTEGER NOT NULL, private_key TEXT NOT NULL, public_key TEXT NOT NULL, preshared_key TEXT NOT NULL DEFAULT '', address TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(inbound_tag,user_id,device_index), UNIQUE(inbound_tag,address));
INSERT INTO users VALUES (7,'awg-user',10,'active',0,0,0,1,0,0,0);
INSERT INTO proxies VALUES (7,'amneziawg');
INSERT INTO hosts VALUES (1,'awg-in',0);
INSERT INTO service_hosts VALUES (10,1);
		INSERT INTO inbounds (tag,uplink,downlink,usage_coefficient) VALUES ('awg-in',0,0,1);
		INSERT INTO xray_config VALUES (1,'{"inbounds":[{"tag":"awg-in","protocol":"amneziawg","port":51821,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.73.0.0/24","server_address":"10.73.0.1/24"}}]}');
		INSERT INTO nodes VALUES (7,'awg-node','127.0.0.1',0,0,'connected','','','native-cert','key','custom','{"inbounds":[{"tag":"awg-in","protocol":"amneziawg","port":51821,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.73.0.0/24","server_address":"10.73.0.1/24"}}]}',1);`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_PUBLIC_URL", "https://panel.example.test")
	tlsFixture := httptest.NewTLSServer(nil)
	certificate := tlsFixture.TLS.Certificates[0]
	tlsFixture.Close()
	rootCert, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(rootCert)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &wireGuardMutationRPCRecorder{
		removed: make(chan *nodev1.RemoveInboundUserRequest, 1),
		added:   make(chan *nodev1.InboundUserRequest, 1),
		updated: make(chan *nodev1.InboundUserRequest, 1),
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})))
	nodev1.RegisterNodeRuntimeServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	client, err := nodeclient.Dial(ctx, listener.Addr().String(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetHandshake("test", []string{"targeted_user_add_remove_amneziawg", "targeted_user_update_amneziawg"})
	raw := []byte(`{"inbounds":[{"tag":"awg-in","protocol":"amneziawg","port":51821,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.73.0.0/24","server_address":"10.73.0.1/24"}}]}`)
	controller := NewController(NewRepository(db, "sqlite"))
	node := NodeRow{ID: 7, Certificate: "native-cert", XrayConfigMode: "custom", XrayConfig: raw}
	for _, operation := range []OperationRow{
		{ID: 91, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}},
		{ID: 92, OperationType: "update_user", UserID: sql.NullInt64{Int64: 7, Valid: true}},
		{ID: 93, OperationType: "remove_user", UserID: sql.NullInt64{Int64: 7, Valid: true}},
	} {
		if err := controller.grpcApplyUserOperation(ctx, client, node, operation, nil); err != nil {
			t.Fatalf("%s targeted AmneziaWG RPC: %v", operation.OperationType, err)
		}
		id := fmt.Sprintf("%s-%d-awg-in", operation.OperationType, operation.ID)
		switch operation.OperationType {
		case "add_user", "update_user":
			channel := recorder.added
			if operation.OperationType == "update_user" {
				channel = recorder.updated
			}
			select {
			case request := <-channel:
				if request.GetOperationId() != id || request.GetInboundTag() != "awg-in" || request.GetUser().GetProtocol() != "amneziawg" {
					t.Fatalf("%s request=%+v", operation.OperationType, request)
				}
				userID, mutation, peers, decodeErr := decodeTestAmneziaWGMutation(request.GetUser())
				if decodeErr != nil || userID != 7 || mutation != strings.TrimSuffix(operation.OperationType, "_user") || len(peers) != 1 || peers[0].UserID != 7 {
					t.Fatalf("%s payload user=%d mutation=%s peers=%+v err=%v", operation.OperationType, userID, mutation, peers, decodeErr)
				}
			case <-ctx.Done():
				t.Fatalf("%s targeted request was not sent", operation.OperationType)
			}
		case "remove_user":
			select {
			case request := <-recorder.removed:
				if request.GetOperationId() != id || request.GetInboundTag() != "awg-in" || request.GetEmail() != "amneziawg-user:7" {
					t.Fatalf("remove request=%+v", request)
				}
			case <-ctx.Done():
				t.Fatal("targeted RemoveUser request was not sent")
			}
		}
	}
}

func decodeTestAmneziaWGMutation(user *nodev1.InboundUser) (int64, string, []AWGRuntimePeer, error) {
	fields := user.GetFields()
	var peers []AWGRuntimePeer
	err := json.Unmarshal([]byte(fields[grpcAmneziaWGPeersField]), &peers)
	var id int64
	_, _ = fmt.Sscan(fields[grpcAmneziaWGUserIDField], &id)
	return id, fields[grpcAmneziaWGOpField], peers, err
}
