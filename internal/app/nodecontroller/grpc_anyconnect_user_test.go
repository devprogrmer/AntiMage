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
	"strings"
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/nodeclient"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "modernc.org/sqlite"
)

type anyConnectMutationRPCRecorder struct {
	nodev1.UnimplementedNodeRuntimeServiceServer
	requests chan any
	ack      *nodev1.RuntimeActionResponse
	ackSet   bool
}

func (r *anyConnectMutationRPCRecorder) AddUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return anyConnectMutationAck(r, req.GetOperationId()), nil
}
func (r *anyConnectMutationRPCRecorder) UpdateUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return anyConnectMutationAck(r, req.GetOperationId()), nil
}
func (r *anyConnectMutationRPCRecorder) RemoveUser(_ context.Context, req *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return anyConnectMutationAck(r, req.GetOperationId()), nil
}
func (r *anyConnectMutationRPCRecorder) SyncConfig(_ context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	fence := req.GetFence()
	return &nodev1.RuntimeActionResponse{
		OperationId: req.GetOperationId(), Accepted: true,
		CommandId: fence.GetCommandId(), ResourceId: fence.GetResourceId(),
		ResourceGeneration: fence.GetResourceGeneration(), LeaseGeneration: fence.GetLeaseGeneration(),
		Runtime: &nodev1.RuntimeState{Connected: true, Started: true, AppliedRevision: req.GetDesiredRevision(), Capabilities: []string{"shared_fencing_v1", "command_idempotency_v1"}},
	}, nil
}

func anyConnectMutationAck(r *anyConnectMutationRPCRecorder, operationID string) *nodev1.RuntimeActionResponse {
	if r.ackSet {
		return r.ack
	}
	return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: operationID}
}

func anyConnectControllerFixture(t *testing.T) (context.Context, *Controller, *nodeclient.Client, *anyConnectMutationRPCRecorder, NodeRow) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "anyconnect-targeted.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, credential_key TEXT, service_id INTEGER, status TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 1, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE xray_config (id INTEGER PRIMARY KEY, data TEXT);
CREATE TABLE inbounds (tag TEXT PRIMARY KEY, uplink INTEGER DEFAULT 0, downlink INTEGER DEFAULT 0, usage_coefficient REAL DEFAULT 1);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, name TEXT, address TEXT, port INTEGER, api_port INTEGER, status TEXT, xray_version TEXT, message TEXT, certificate TEXT, certificate_key TEXT, xray_config_mode TEXT, xray_config TEXT, usage_coefficient REAL DEFAULT 1, applied_revision INTEGER DEFAULT 0, desired_revision INTEGER DEFAULT 0, node_capabilities TEXT DEFAULT '["shared_fencing_v1","command_idempotency_v1"]', last_seen_at TEXT, agent_status TEXT DEFAULT '', xray_status TEXT DEFAULT '', last_status_change TEXT);
CREATE TABLE wireguard_peer_addresses (inbound_tag TEXT NOT NULL, user_id INTEGER NOT NULL, pool TEXT NOT NULL, server_address TEXT NOT NULL, address TEXT NOT NULL, PRIMARY KEY (inbound_tag, user_id), UNIQUE (inbound_tag, address));
CREATE TABLE node_wireguard_usage_reflection (node_id INTEGER, user_id INTEGER, batch_id TEXT, ledger_json TEXT NULL, updated_at DATETIME, PRIMARY KEY(node_id,user_id));
INSERT INTO users VALUES (7,'ac-user','0123456789abcdef0123456789abcdef',10,'active',0,0,0,1,0,0,0);
INSERT INTO proxies VALUES (7,'anyconnect');
INSERT INTO hosts VALUES (1,'ac-in',0);
INSERT INTO service_hosts VALUES (10,1);
INSERT INTO inbounds VALUES ('ac-in',0,0,1);
INSERT INTO xray_config VALUES (1,'{"inbounds":[{"tag":"ac-in","protocol":"anyconnect","port":443,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.71.0.0/24"}}]}');
INSERT INTO nodes (id,name,address,port,api_port,status,message,certificate,certificate_key,xray_config_mode,xray_config,usage_coefficient) VALUES (7,'ac-node','127.0.0.1',0,0,'connected','','native-cert','key','custom','{"inbounds":[{"tag":"ac-in","protocol":"anyconnect","port":443,"targets":["node:7"],"settings":{"ipv4_pool_cidr":"10.71.0.0/24"}}]}',1);`); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		operationapp.ExecutorLeaseDDL,
		operationapp.ResourceFenceDDL,
		`CREATE TABLE operations (id TEXT PRIMARY KEY,operation_type TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,requested_by TEXT NOT NULL,request_id TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,progress INTEGER,created_at BIGINT NOT NULL,started_at BIGINT,updated_at BIGINT NOT NULL,completed_at BIGINT,error TEXT NOT NULL,metadata_json TEXT NOT NULL)`,
		`CREATE TABLE operation_locks (target_type TEXT,target_id TEXT,operation_id TEXT UNIQUE,PRIMARY KEY(target_type,target_id))`,
		`CREATE TABLE operation_events (operation_id TEXT,sequence INTEGER,state TEXT,phase TEXT,observed_at BIGINT,requested_by TEXT,request_id TEXT,event_type TEXT NOT NULL DEFAULT 'operation.transition',payload_json TEXT,PRIMARY KEY(operation_id,sequence))`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
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
	recorder := &anyConnectMutationRPCRecorder{requests: make(chan any, 8)}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})))
	nodev1.RegisterNodeRuntimeServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	client, err := nodeclient.Dial(ctx, listener.Addr().String(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	controller := NewController(NewRepository(db, "sqlite"))
	return ctx, &controller, client, recorder, NodeRow{ID: 7, Certificate: "native-cert", XrayConfigMode: "custom"}
}

func TestAnyConnectMutationRequestCarriesDesiredStateAndRevision(t *testing.T) {
	user := RemoteAccessRuntimeUser{UserID: 7, Username: "alice", Password: "secret", Status: "active", UsedTraffic: 123, UsageCoefficient: 1.5, InboundCoefficient: 2}
	req, err := grpcAnyConnectMutationUser(user, "update", 91)
	if err != nil {
		t.Fatal(err)
	}
	f := req.GetFields()
	if req.GetProtocol() != "anyconnect" || f[grpcAnyConnectUserIDField] != "7" || f[grpcAnyConnectOperationField] != "update" || f[grpcAnyConnectRevisionField] != "91" {
		t.Fatalf("envelope=%v", f)
	}
	var got RemoteAccessRuntimeUser
	if err := json.Unmarshal([]byte(f[grpcAnyConnectUserJSONField]), &got); err != nil || got.UserID != 7 || got.Password != "secret" || got.UsageCoefficient != 1.5 {
		t.Fatalf("decoded=%+v err=%v", got, err)
	}
}

func TestAnyConnectCapabilityMappingRemainsExplicitAndControllerFallbackIsConservative(t *testing.T) {
	if got := targetedUserCapability("anyconnect", "update_user"); got != "targeted_user_update_anyconnect" {
		t.Fatalf("update capability=%q", got)
	}
	if got := targetedUserCapability("cisco", "add_user"); got != "targeted_user_add_remove_anyconnect" {
		t.Fatalf("cisco add capability=%q", got)
	}
	if got := targetedUserCapability("anyconnect", "remove_user"); got != "targeted_user_add_remove_anyconnect" {
		t.Fatalf("remove capability=%q", got)
	}
	if supportsTargetedUserMutation(&nodeclient.Client{}, []string{"anyconnect"}, "add_user", true) {
		t.Fatal("unhandshaken node was targeted")
	}
}

func TestAnyConnectLegacyNodeUsesFallbackAndCapableNodeUsesTargetedOnly(t *testing.T) {
	legacy := &nodeclient.Client{}
	if supportsTargetedUserMutation(legacy, []string{"anyconnect"}, "add_user", true) {
		t.Fatal("legacy AnyConnect node selected targeted path")
	}
	capable := &nodeclient.Client{}
	capable.SetHandshake("test", []string{"targeted_user_add_remove_anyconnect", "targeted_user_update_anyconnect"})
	if !supportsTargetedUserMutation(capable, []string{"anyconnect"}, "add_user", true) {
		t.Fatal("capable AnyConnect node did not select targeted add")
	}
	if !supportsTargetedUserMutation(capable, []string{"anyconnect"}, "update_user", true) {
		t.Fatal("capable AnyConnect node did not select targeted update")
	}
	if supportsTargetedUserMutation(capable, []string{"anyconnect", "openvpn"}, "add_user", true) {
		t.Fatal("mixed protocol mutation was not conservative")
	}
}

func TestControllerAnyConnectUsesTargetedRPCsAndAcknowledgements(t *testing.T) {
	ctx, controller, client, recorder, node := anyConnectControllerFixture(t)
	client.SetHandshake("test", []string{"targeted_user_add_remove_anyconnect", "targeted_user_update_anyconnect"})
	for _, operation := range []string{"add_user", "update_user", "remove_user"} {
		row := OperationRow{ID: 91, OperationType: operation, UserID: sql.NullInt64{Int64: 7, Valid: true}}
		if err := controller.grpcApplyUserOperation(ctx, client, node, row, nil); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		select {
		case got := <-recorder.requests:
			switch req := got.(type) {
			case *nodev1.InboundUserRequest:
				want := "add"
				if operation == "update_user" {
					want = "update"
				}
				if req.GetInboundTag() != "ac-in" || req.GetOperationId() != operation+"-91-ac-in" || req.GetUser().GetProtocol() != "anyconnect" || req.GetUser().GetFields()[grpcAnyConnectOperationField] != want {
					t.Fatalf("%s targeted RPC=%+v", operation, req)
				}
				var desired RemoteAccessRuntimeUser
				if err := json.Unmarshal([]byte(req.GetUser().GetFields()[grpcAnyConnectUserJSONField]), &desired); err != nil || desired.Password == "" || desired.UserID != 7 {
					t.Fatalf("AnyConnect desired user=%+v err=%v", desired, err)
				}
			case *nodev1.RemoveInboundUserRequest:
				if operation != "remove_user" || req.GetInboundTag() != "ac-in" || req.GetOperationId() != operation+"-91-ac-in" || !strings.HasPrefix(req.GetEmail(), grpcAnyConnectRemovePrefix) {
					t.Fatalf("remove targeted RPC=%+v", req)
				}
			default:
				t.Fatalf("unexpected RPC %T", got)
			}
		case <-ctx.Done():
			t.Fatalf("%s targeted RPC was not emitted", operation)
		}
	}
}

func TestControllerAnyConnectRejectsInvalidAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name string
		ack  *nodev1.RuntimeActionResponse
	}{
		{name: "missing"},
		{name: "rejected", ack: &nodev1.RuntimeActionResponse{OperationId: "add_user-91-ac-in"}},
		{name: "mismatched operation", ack: &nodev1.RuntimeActionResponse{Accepted: true, OperationId: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, controller, client, recorder, node := anyConnectControllerFixture(t)
			client.SetHandshake("test", []string{"targeted_user_add_remove_anyconnect"})
			recorder.ackSet = true
			recorder.ack = test.ack
			err := controller.grpcApplyUserOperation(ctx, client, node, OperationRow{ID: 91, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}, nil)
			if err == nil {
				t.Fatal("invalid targeted AnyConnect acknowledgement was accepted")
			}
		})
	}
}

func TestControllerAnyConnectLegacyNodeFallsBackToFullSync(t *testing.T) {
	ctx, controller, client, recorder, node := anyConnectControllerFixture(t)
	// No handshake capabilities are advertised, so the controller must take the
	// conservative full-sync fallback path instead of a targeted mutation.
	if err := controller.grpcApplyUserOperation(ctx, client, node, OperationRow{ID: 91, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}, nil); err != nil {
		t.Fatalf("legacy AnyConnect fallback: %v", err)
	}
	select {
	case got := <-recorder.requests:
		if _, ok := got.(*nodev1.RuntimeConfigRequest); !ok {
			t.Fatalf("legacy node did not receive a full sync request: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("legacy AnyConnect fallback emitted no RPC")
	}
}

func TestControllerAnyConnectTargetedMutationDoesNotFallBack(t *testing.T) {
	ctx, controller, client, recorder, node := anyConnectControllerFixture(t)
	client.SetHandshake("test", []string{"targeted_user_add_remove_anyconnect"})
	if err := controller.grpcApplyUserOperation(ctx, client, node, OperationRow{ID: 91, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}, nil); err != nil {
		t.Fatalf("targeted AnyConnect mutation: %v", err)
	}
	// The recorder's request channel must contain exactly one targeted request,
	// proving a successful targeted mutation never also emits a full sync.
	select {
	case got := <-recorder.requests:
		if _, ok := got.(*nodev1.InboundUserRequest); !ok {
			t.Fatalf("targeted path did not emit an InboundUserRequest: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("targeted AnyConnect mutation emitted no RPC")
	}
	select {
	case extra := <-recorder.requests:
		t.Fatalf("targeted AnyConnect mutation unexpectedly emitted a second RPC: %+v", extra)
	default:
	}
}
