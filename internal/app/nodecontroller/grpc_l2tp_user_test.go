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
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "modernc.org/sqlite"
)

type l2TPMutationRPCRecorder struct {
	nodev1.UnimplementedNodeRuntimeServiceServer
	requests chan any
	ack      *nodev1.RuntimeActionResponse
	ackSet   bool
}

func (r *l2TPMutationRPCRecorder) AddUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return l2TPMutationAck(r, req.GetOperationId()), nil
}
func (r *l2TPMutationRPCRecorder) UpdateUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return l2TPMutationAck(r, req.GetOperationId()), nil
}
func (r *l2TPMutationRPCRecorder) RemoveUser(_ context.Context, req *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return l2TPMutationAck(r, req.GetOperationId()), nil
}
func (r *l2TPMutationRPCRecorder) SyncConfig(_ context.Context, req *nodev1.RuntimeConfigRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	fence := req.GetFence()
	return &nodev1.RuntimeActionResponse{
		OperationId: req.GetOperationId(), Accepted: true,
		CommandId: fence.GetCommandId(), ResourceId: fence.GetResourceId(),
		ResourceGeneration: fence.GetResourceGeneration(), LeaseGeneration: fence.GetLeaseGeneration(),
		Runtime: &nodev1.RuntimeState{Connected: true, Started: true, AppliedRevision: req.GetDesiredRevision(), Capabilities: []string{"shared_fencing_v1", "command_idempotency_v1"}},
	}, nil
}

func l2TPMutationAck(r *l2TPMutationRPCRecorder, operationID string) *nodev1.RuntimeActionResponse {
	if r.ackSet {
		return r.ack
	}
	return &nodev1.RuntimeActionResponse{Accepted: true, OperationId: operationID}
}

func l2TPControllerFixture(t *testing.T) (context.Context, *Controller, *nodeclient.Client, *l2TPMutationRPCRecorder, NodeRow) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "l2tp-targeted.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, credential_key TEXT, service_id INTEGER, status TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 0, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, status TEXT DEFAULT 'connected', last_status_change TEXT, message TEXT DEFAULT '', xray_version TEXT DEFAULT '', agent_status TEXT DEFAULT '', xray_status TEXT DEFAULT '', applied_revision INTEGER DEFAULT 0, desired_revision INTEGER DEFAULT 0, last_seen_at TEXT, node_capabilities TEXT DEFAULT '["shared_fencing_v1","command_idempotency_v1"]');
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE node_wireguard_usage_reflection (node_id INTEGER, user_id INTEGER, batch_id TEXT, ledger_json TEXT NULL, updated_at DATETIME, PRIMARY KEY(node_id,user_id));
INSERT INTO users (id,username,credential_key,service_id,status,used_traffic,data_limit,expire,device_limit,ip_limit,upload_speed_limit,download_speed_limit) VALUES (7,'l2tp-user','0123456789abcdef0123456789abcdef',10,'active',123,100000,0,2,3,4000,8000);
INSERT INTO nodes (id) VALUES (7);
INSERT INTO proxies VALUES (7,'l2tp');
INSERT INTO hosts VALUES (1,'l2tp-in',0);
INSERT INTO service_hosts VALUES (10,1);
INSERT INTO node_wireguard_usage_reflection (node_id,user_id,batch_id) VALUES (7,7,'batch-current');`)
	if err != nil {
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
	serverConfig, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": "l2tp-in", "protocol": "l2tp", "targets": []any{"node:7"},
		"usage_coefficient": 2.0,
		"settings":          map[string]any{"ipv4_pool_cidr": "10.77.0.0/24"},
	}}})
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
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})))
	recorder := &l2TPMutationRPCRecorder{requests: make(chan any, 8)}
	nodev1.RegisterNodeRuntimeServiceServer(grpcServer, recorder)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	t.Cleanup(cancel)
	client, err := nodeclient.Dial(callCtx, listener.Addr().String(), &tls.Config{RootCAs: rootPool, MinVersion: tls.VersionTLS12}, grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	controller := NewController(NewRepository(db, "sqlite"))
	return callCtx, &controller, client, recorder, NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: serverConfig, UsageCoefficient: 1.5}
}

func TestL2TPMutationRequestCarriesDesiredStateAndRevision(t *testing.T) {
	user := L2TPRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "secret", Status: "active"}
	request, err := grpcL2TPMutationUser(user, "update", 91)
	if err != nil {
		t.Fatal(err)
	}
	fields := request.GetFields()
	if request.GetProtocol() != "l2tp" || fields[grpcL2TPUserIDField] != "7" || fields[grpcL2TPOperationField] != "update" || fields[grpcL2TPRevisionField] != "91" {
		t.Fatalf("L2TP mutation envelope=%+v", request)
	}
	var decoded L2TPRuntimeUser
	if err := json.Unmarshal([]byte(fields[grpcL2TPUserJSONField]), &decoded); err != nil || decoded.UserID != user.UserID || decoded.Password != user.Password {
		t.Fatalf("encoded desired L2TP user=%+v err=%v", decoded, err)
	}
}

func TestControllerL2TPUsesTargetedRPCsAndAcknowledgements(t *testing.T) {
	ctx, controller, client, recorder, node := l2TPControllerFixture(t)
	client.SetHandshake("test", []string{"targeted_user_add_remove_l2tp", "targeted_user_update_l2tp"})
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
				if operation == "remove_user" || req.GetInboundTag() != "l2tp-in" || req.GetOperationId() != operation+"-91-l2tp-in" || req.GetUser().GetProtocol() != "l2tp" || req.GetUser().GetFields()[grpcL2TPOperationField] != want {
					t.Fatalf("%s targeted RPC=%+v", operation, req)
				}
				var desired L2TPRuntimeUser
				if err := json.Unmarshal([]byte(req.GetUser().GetFields()[grpcL2TPUserJSONField]), &desired); err != nil || desired.Password == "" || desired.UsageCoefficient != 1.5 || desired.InboundCoefficient != 2.0 || desired.UsedTraffic != 123 || desired.ReflectedUsageBatchID != "batch-current" {
					t.Fatalf("L2TP desired user=%+v err=%v", desired, err)
				}
			case *nodev1.RemoveInboundUserRequest:
				if operation != "remove_user" || req.GetInboundTag() != "l2tp-in" || req.GetEmail() != "l2tp-user:7:91" || req.GetOperationId() != operation+"-91-l2tp-in" {
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

func TestControllerL2TPRejectsInvalidAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name string
		ack  *nodev1.RuntimeActionResponse
	}{
		{name: "missing"},
		{name: "rejected", ack: &nodev1.RuntimeActionResponse{OperationId: "add_user-91-l2tp-in"}},
		{name: "mismatched operation", ack: &nodev1.RuntimeActionResponse{Accepted: true, OperationId: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, controller, client, recorder, node := l2TPControllerFixture(t)
			client.SetHandshake("test", []string{"targeted_user_add_remove_l2tp"})
			recorder.ackSet = true
			recorder.ack = test.ack
			err := controller.grpcApplyUserOperation(ctx, client, node, OperationRow{ID: 91, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}, nil)
			if err == nil {
				t.Fatal("invalid targeted L2TP acknowledgement was accepted")
			}
		})
	}
}

func TestLegacyL2TPNodeUsesFencedFullSyncFallback(t *testing.T) {
	ctx, controller, client, recorder, node := l2TPControllerFixture(t)
	prepared := &preparedRuntimeConfig{nodeID: node.ID, configJSON: `{}`, ovRuntimeJSON: `{"l2tp_inbounds":[]}`}
	err := controller.grpcApplyUserOperation(ctx, client, node, OperationRow{ID: 92, OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}, prepared)
	if err != nil {
		t.Fatalf("legacy full-sync fallback: %v", err)
	}
	select {
	case request := <-recorder.requests:
		syncRequest, ok := request.(*nodev1.RuntimeConfigRequest)
		if !ok {
			t.Fatalf("legacy node received targeted mutation request %T", request)
		}
		if syncRequest.GetOperationId() != "legacy-full-sync-add_user-92" || syncRequest.GetConfigJson() != `{}` || syncRequest.GetFence() == nil {
			t.Fatalf("legacy fallback did not use fenced full SyncConfig: %+v", syncRequest)
		}
	case <-ctx.Done():
		t.Fatal("legacy node did not receive the full-sync fallback")
	}
}

func TestL2TPTargetedCapabilityIsOperationSpecificAndConservative(t *testing.T) {
	client := &nodeclient.Client{}
	client.SetHandshake("test", []string{"targeted_user_add_remove_l2tp", "targeted_user_update_l2tp"})
	for _, operation := range []string{"add_user", "remove_user", "enable_user", "disable_user"} {
		if got := targetedUserCapability("l2tp", operation); got != "targeted_user_add_remove_l2tp" {
			t.Fatalf("%s capability=%q", operation, got)
		}
	}
	if got := targetedUserCapability("l2tp", "update_user"); got != "targeted_user_update_l2tp" {
		t.Fatalf("update capability=%q", got)
	}
	if supportsTargetedUserMutation(client, []string{"l2tp"}, "add_user", true) != true {
		t.Fatal("capable assigned L2TP was not targeted")
	}
	if supportsTargetedUserMutation(client, []string{"l2tp"}, "add_user", false) {
		t.Fatal("unassigned node was targeted")
	}
	if supportsTargetedUserMutation(client, []string{"l2tp", "openvpn"}, "add_user", true) {
		t.Fatal("mixed protocols were targeted")
	}
	legacy := &nodeclient.Client{}
	if supportsTargetedUserMutation(legacy, []string{"l2tp"}, "add_user", true) {
		t.Fatal("legacy L2TP node was targeted")
	}
}
