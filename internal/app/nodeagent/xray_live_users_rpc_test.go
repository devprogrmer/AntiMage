package nodeagent

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func xrayLiveRPCFixture(t *testing.T) (*Server, *int, *bool, *bool) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	users := map[string]proto.Message{}
	var mu sync.Mutex
	adds := 0
	failSecond := false
	unsupportedGet := false
	grpcServer := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(method, "/QueryStats"):
			req := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsRequest"))
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			return stream.SendMsg(dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsResponse")))
		case strings.HasSuffix(method, "/GetInboundUsers"):
			if unsupportedGet {
				return status.Error(codes.Unimplemented, "older Xray lacks GetInboundUsers")
			}
			req := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserRequest"))
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			tag := req.Get(req.Descriptor().Fields().ByNumber(1)).String()
			email := req.Get(req.Descriptor().Fields().ByNumber(2)).String()
			response := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserResponse"))
			if user := users[tag+"\x00"+email]; user != nil {
				response.Mutable(response.Descriptor().Fields().ByNumber(1)).List().Append(protoreflect.ValueOfMessage(user.ProtoReflect()))
			}
			return stream.SendMsg(response)
		case strings.HasSuffix(method, "/AlterInbound"):
			req := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
			if err := stream.RecvMsg(req); err != nil {
				return err
			}
			tag := req.Get(req.Descriptor().Fields().ByNumber(1)).String()
			typed := req.Get(req.Descriptor().Fields().ByNumber(2)).Message()
			if typed.Get(typed.Descriptor().Fields().ByNumber(1)).String() != "xray.app.proxyman.command.AddUserOperation" {
				return status.Error(codes.InvalidArgument, "wrong operation type")
			}
			operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AddUserOperation"))
			if err := proto.Unmarshal(typed.Get(typed.Descriptor().Fields().ByNumber(2)).Bytes(), operation); err != nil {
				return err
			}
			user := operation.Get(operation.Descriptor().Fields().ByNumber(1)).Message()
			email := user.Get(user.Descriptor().Fields().ByNumber(2)).String()
			if failSecond && email == "three" {
				failSecond = false
				return status.Error(codes.Unavailable, "injected core failure")
			}
			key := tag + "\x00" + email
			if users[key] != nil {
				return status.Error(codes.AlreadyExists, "duplicate email")
			}
			users[key] = proto.Clone(user.Interface())
			adds++
			return stream.SendMsg(dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse")))
		}
		return status.Error(codes.Unimplemented, "unexpected RPC")
	}))
	go grpcServer.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///xray-fixture", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); grpcServer.Stop(); listener.Close() })
	s := New(Config{DataDir: t.TempDir(), XrayAPIPort: 12345})
	s.cfg.XrayPath = filepath.Join(s.cfg.DataDir, "fixture-core")
	if err := os.WriteFile(s.cfg.XrayPath, []byte("test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	s.lastRuntime = &exec.Cmd{}
	s.xrayRuntimeGeneration = "retained-generation"
	s.lastConfig = filepath.Join(s.cfg.DataDir, "xray-config.json")
	s.xrayStatsClient = newXrayStatsClient(s.cfg.XrayPath, s.cfg.XrayAPIPort)
	s.xrayStatsClient.rpc.conn = conn
	oldCommand := xrayCommandContext
	xrayCommandContext = func(context.Context, string, ...string) *exec.Cmd {
		t.Error("live addition attempted a global Xray restart")
		return exec.Command("not-a-real-runtime")
	}
	t.Cleanup(func() { xrayCommandContext = oldCommand })
	return s, &adds, &failSecond, &unsupportedGet
}

func TestXrayLiveUnsupportedReadFallsBackBeforeMutation(t *testing.T) {
	s, adds, _, unsupportedGet := xrayLiveRPCFixture(t)
	*unsupportedGet = true
	old := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[]}}]}`
	desired := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[{"email":"two","id":"22222222-2222-4222-8222-222222222222"}]}}]}`
	if err := os.WriteFile(s.lastConfig, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	usedLive, err := s.tryLiveXrayUserAdditions(context.Background(), desired)
	if err != nil || usedLive || *adds != 0 {
		t.Fatalf("older core should use prior full apply before any live mutation: usedLive=%v adds=%d err=%v", usedLive, *adds, err)
	}
}

func TestXrayLiveSyncPersistsPolicyAndRetainsRuntime(t *testing.T) {
	s, adds, _, _ := xrayLiveRPCFixture(t)
	old := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[]}}]}`
	desired := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[{"email":"two","id":"22222222-2222-4222-8222-222222222222"}]}}]}`
	if err := os.WriteFile(s.lastConfig, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	req := &nodev1.RuntimeConfigRequest{OperationId: "add-two", ConfigJson: desired, OvRuntimeJson: `{"xray_policies":[{"user_id":2,"email":"two","inbound_tag":"vless","status":"active"}]}`, DesiredRevision: 2}
	witness := s.lastRuntime
	for i := 0; i < 2; i++ {
		res, err := s.SyncConfig(context.Background(), req)
		if err != nil || !res.GetAccepted() {
			t.Fatalf("sync/retry: %v %v", res, err)
		}
	}
	if *adds != 1 || s.lastRuntime != witness || s.xrayRuntimeGeneration != "retained-generation" {
		t.Fatal("retry duplicated user or reset witness runtime")
	}
	raw, err := os.ReadFile(s.lastConfig)
	if err != nil || string(raw) != desired {
		t.Fatal("desired config was not persisted")
	}
	restarted := New(s.cfg)
	snapshot, err := restarted.loadRuntimePolicy()
	if err != nil || snapshot.ConfigJSON != desired || snapshot.Revision != 2 {
		t.Fatal("restart lost new authoritative policy")
	}
}

func TestXrayLivePartialFailureRetriesWithoutRestart(t *testing.T) {
	s, adds, failSecond, _ := xrayLiveRPCFixture(t)
	*failSecond = true
	old := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[]}}]}`
	desired := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[{"email":"two","id":"22222222-2222-4222-8222-222222222222"},{"email":"three","id":"33333333-3333-4333-8333-333333333333"}]}}]}`
	if err := os.WriteFile(s.lastConfig, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	req := &nodev1.RuntimeConfigRequest{ConfigJson: desired, DesiredRevision: 3}
	if _, err := s.SyncConfig(context.Background(), req); err == nil {
		t.Fatal("partial core failure acknowledged")
	}
	raw, _ := os.ReadFile(s.lastConfig)
	if string(raw) != old {
		t.Fatal("failed apply replaced last completely applied core config")
	}
	if _, err := s.SyncConfig(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if *adds != 2 || s.xrayRuntimeGeneration != "retained-generation" {
		t.Fatal("retry lost/doubled additions or reset core")
	}
}

func TestXrayLiveSyncKeepsOtherUsersPendingQuotaCutoffs(t *testing.T) {
	s, _, _, _ := xrayLiveRPCFixture(t)
	config := `{"inbounds":[{"tag":"vless","protocol":"vless","settings":{"decryption":"none","clients":[]}}]}`
	if err := os.WriteFile(s.lastConfig, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	s.xrayQuotaRevoked = map[string]xrayQuotaRevocation{"spent": {Generation: s.xrayRuntimeGeneration, ExistingStreams: true}}
	if _, err := s.SyncConfig(context.Background(), &nodev1.RuntimeConfigRequest{ConfigJson: config, OvRuntimeJson: `{"xray_policies":[{"user_id":9,"email":"spent","inbound_tag":"vless","status":"disabled"}]}`, DesiredRevision: 4}); err != nil {
		t.Fatal(err)
	}
	if marker, ok := s.xrayQuotaRevoked["spent"]; !ok || !marker.ExistingStreams {
		t.Fatal("live config incorrectly treated a quota cutoff as completed by restart")
	}
}
