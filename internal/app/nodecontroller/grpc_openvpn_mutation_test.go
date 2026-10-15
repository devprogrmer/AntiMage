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

type openVPNMutationRPCRecorder struct {
	nodev1.UnimplementedNodeRuntimeServiceServer
	requests chan any
}

func (r *openVPNMutationRPCRecorder) AddUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return &nodev1.RuntimeActionResponse{Accepted: true}, nil
}

func (r *openVPNMutationRPCRecorder) UpdateUser(_ context.Context, req *nodev1.InboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return &nodev1.RuntimeActionResponse{Accepted: true}, nil
}

func (r *openVPNMutationRPCRecorder) RemoveUser(_ context.Context, req *nodev1.RemoveInboundUserRequest) (*nodev1.RuntimeActionResponse, error) {
	r.requests <- req
	return &nodev1.RuntimeActionResponse{Accepted: true}, nil
}

func openVPNControllerFixture(t *testing.T) (context.Context, *Controller, *nodeclient.Client, *openVPNMutationRPCRecorder, NodeRow) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "openvpn-targeted.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, credential_key TEXT, service_id INTEGER, status TEXT, used_traffic INTEGER DEFAULT 0, data_limit INTEGER, expire INTEGER, device_limit INTEGER DEFAULT 0, ip_limit INTEGER DEFAULT 0, upload_speed_limit INTEGER DEFAULT 0, download_speed_limit INTEGER DEFAULT 0);
CREATE TABLE nodes (id INTEGER PRIMARY KEY, node_capabilities TEXT DEFAULT '[]');
CREATE TABLE proxies (user_id INTEGER, type TEXT);
CREATE TABLE hosts (id INTEGER PRIMARY KEY, inbound_tag TEXT, is_disabled BOOLEAN DEFAULT 0);
CREATE TABLE service_hosts (service_id INTEGER, host_id INTEGER);
CREATE TABLE wireguard_peer_addresses (inbound_tag TEXT, user_id INTEGER, pool TEXT, server_address TEXT, address TEXT, PRIMARY KEY (inbound_tag,user_id), UNIQUE(inbound_tag,address));
INSERT INTO users (id,username,credential_key,service_id,status,used_traffic,data_limit,expire,device_limit,ip_limit,upload_speed_limit,download_speed_limit) VALUES (7,'ov-user','0123456789abcdef0123456789abcdef',10,'active',123,100000,0,2,3,4000,8000);
INSERT INTO nodes (id,node_capabilities) VALUES (7,'[]');
INSERT INTO proxies VALUES (7,'openvpn');
INSERT INTO hosts VALUES (1,'ov-in',0);
INSERT INTO service_hosts VALUES (10,1);`)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{
		"tag": "ov-in", "protocol": "openvpn", "targets": []any{"node:7"},
		"settings": map[string]any{"ipv4_pool_cidr": "10.77.0.0/24"},
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
	recorder := &openVPNMutationRPCRecorder{requests: make(chan any, 4)}
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
	return callCtx, &controller, client, recorder, NodeRow{ID: 7, XrayConfigMode: "custom", XrayConfig: serverConfig}
}

func TestOpenVPNMutationRequestCarriesVersionedDesiredState(t *testing.T) {
	user := OVRuntimeUser{UserID: 7, Username: "alice", VPNUsername: "alice", Password: "pass", Status: "active", UsedTraffic: 40}
	request, err := grpcOpenVPNMutationUser(user, "update", 91)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetProtocol() != "openvpn" || request.GetFields()[grpcOpenVPNUserIDField] != "7" || request.GetFields()[grpcOpenVPNOperationField] != "update" || request.GetFields()[grpcOpenVPNRevisionField] != "91" {
		t.Fatalf("OpenVPN mutation envelope=%+v", request)
	}
	var decoded OVRuntimeUser
	if err := json.Unmarshal([]byte(request.GetFields()[grpcOpenVPNUserJSONField]), &decoded); err != nil || decoded.UserID != 7 || decoded.Password != user.Password || decoded.UsedTraffic != user.UsedTraffic {
		t.Fatalf("encoded desired OpenVPN user=%+v err=%v", decoded, err)
	}
	if _, err := grpcOpenVPNMutationUser(OVRuntimeUser{UserID: 7}, "add", 91); err == nil {
		t.Fatal("OpenVPN add accepted missing credentials")
	}
}

func TestControllerOpenVPNSendsTargetedAddUpdateAndRemoveRPCs(t *testing.T) {
	ctx, controller, client, recorder, node := openVPNControllerFixture(t)
	client.SetHandshake("test", []string{"targeted_user_add_remove_openvpn", "targeted_user_update_openvpn"})
	for _, operation := range []string{"add_user", "update_user", "remove_user"} {
		row := OperationRow{ID: 91, OperationType: operation, UserID: sql.NullInt64{Int64: 7, Valid: true}}
		if err := controller.grpcApplyUserOperation(ctx, client, node, row, nil); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		select {
		case got := <-recorder.requests:
			switch request := got.(type) {
			case *nodev1.InboundUserRequest:
				wantOp := "add"
				if operation == "update_user" {
					wantOp = "update"
				}
				if operation == "remove_user" || request.GetInboundTag() != "ov-in" || request.GetOperationId() != operation+"-91-ov-in" || request.GetUser().GetFields()[grpcOpenVPNOperationField] != wantOp {
					t.Fatalf("%s targeted RPC=%+v", operation, request)
				}
			case *nodev1.RemoveInboundUserRequest:
				if operation != "remove_user" || request.GetInboundTag() != "ov-in" || request.GetEmail() != "openvpn-user:7:91" || request.GetOperationId() != operation+"-91-ov-in" {
					t.Fatalf("remove targeted RPC=%+v", request)
				}
			default:
				t.Fatalf("unexpected RPC request type %T", got)
			}
		case <-ctx.Done():
			t.Fatalf("%s targeted RPC was not emitted", operation)
		}
	}
}

func TestControllerLegacyOpenVPNNodeKeepsCapabilityGateClosed(t *testing.T) {
	ctx, controller, client, _, node := openVPNControllerFixture(t)
	if client.Supports(targetedUserCapability("openvpn", "add_user")) {
		t.Fatal("legacy OpenVPN node unexpectedly reports targeted capability")
	}
	// Without the handshake capability, the targeted OpenVPN RPC is not selected.
	if targeted, err := controller.runtimeUserOperationTargeted(ctx, node.ID, OperationRow{OperationType: "add_user", UserID: sql.NullInt64{Int64: 7, Valid: true}}); err != nil || targeted {
		t.Fatalf("legacy target decision targeted=%v err=%v", targeted, err)
	}
}

func TestOpenVPNTargetedCapabilityRequiresAdvertisedAssignedSingleProtocol(t *testing.T) {
	client := &nodeclient.Client{}
	client.SetHandshake("test", []string{"targeted_user_add_remove_openvpn", "targeted_user_update_openvpn"})
	for _, test := range []struct {
		name      string
		protocols []string
		operation string
		assigned  bool
		want      bool
	}{
		{name: "capable assigned add", protocols: []string{"openvpn"}, operation: "add_user", assigned: true, want: true},
		{name: "legacy node", protocols: []string{"openvpn"}, operation: "add_user", assigned: false},
		{name: "mixed protocol remains conservative", protocols: []string{"openvpn", "vless"}, operation: "add_user", assigned: true},
		{name: "unadvertised operation", protocols: []string{"openvpn"}, operation: "sync_config", assigned: true},
		{name: "targeted update", protocols: []string{"openvpn"}, operation: "update_user", assigned: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := supportsTargetedUserMutation(client, test.protocols, test.operation, test.assigned); got != test.want {
				t.Fatalf("targeted=%v want=%v", got, test.want)
			}
		})
	}
}
