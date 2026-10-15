package nodeagent

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestXrayCachedStatsRPCAndRemovalErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if strings.HasSuffix(method, "/AlterInbound") {
			request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
			if err := stream.RecvMsg(request); err != nil {
				return err
			}
			if request.Get(request.Descriptor().Fields().ByNumber(1)).String() != "inbound" {
				return status.Error(codes.InvalidArgument, "wrong tag")
			}
			return status.Error(codes.PermissionDenied, "fixture removal denied")
		}
		request := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsRequest"))
		if err := stream.RecvMsg(request); err != nil {
			return err
		}
		if request.Get(request.Descriptor().Fields().ByNumber(2)).Bool() {
			return status.Error(codes.InvalidArgument, "must not reset counters")
		}
		queries.Add(1)
		response := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsResponse"))
		stat := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("Stat"))
		stat.Set(stat.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("user>>>10.inbound>>>traffic>>>uplink"))
		stat.Set(stat.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfInt64(100))
		response.Mutable(response.Descriptor().Fields().ByNumber(1)).List().Append(protoreflect.ValueOfMessage(stat))
		return stream.SendMsg(response)
	}))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	client := newXrayStatsClient("nonexistent-no-command-required", listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(client.rpc.close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		stats, err := client.queryStats(ctx, "user>>>", false)
		if err != nil || len(stats) != 1 || stats[0].Value != 100 {
			t.Fatalf("stats=%v err=%v", stats, err)
		}
	}
	first, _ := client.rpc.client()
	second, _ := client.rpc.client()
	if first != second || queries.Load() != 2 {
		t.Fatal("connection not reused or queries duplicated")
	}
	if status.Code(client.removeUserRPC(ctx, "inbound", "10.inbound")) != codes.PermissionDenied {
		t.Fatal("native RPC removal failure was hidden")
	}
}

func TestXrayAccountingAndQuotaTickSharesStatsSample(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if strings.HasSuffix(method, "/QueryStats") {
			request := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsRequest"))
			if err := stream.RecvMsg(request); err != nil {
				return err
			}
			queries.Add(1)
			response := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsResponse"))
			stat := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("Stat"))
			stat.Set(stat.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("user>>>7.alice>>>traffic>>>uplink"))
			stat.Set(stat.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfInt64(100))
			response.Mutable(response.Descriptor().Fields().ByNumber(1)).List().Append(protoreflect.ValueOfMessage(stat))
			return stream.SendMsg(response)
		}
		return status.Error(codes.Unimplemented, "unexpected Xray RPC")
	}))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	s := New(Config{DataDir: t.TempDir(), XrayPath: "unused", XrayAPIPort: listener.Addr().(*net.TCPAddr).Port})
	t.Cleanup(s.closeXrayStatsClient)
	s.runtimePolicy = &runtimePolicySnapshot{Revision: 1, Checksum: "one", NativeJSON: `{"xray_policies":[{"user_id":7,"email":"7.alice","inbound_tag":"vless","protocol":"vless","status":"active","data_limit":100000}]}`}
	s.lastRuntime = &exec.Cmd{Process: &os.Process{Pid: 1}}
	s.xrayRuntimeGeneration = "generation-one"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.xrayLocalAccountingTick(ctx, true, true); err != nil {
		t.Fatalf("combined Xray accounting/quota tick: %v", err)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("same-deadline checkpoint+quota issued %d stats queries, want one", got)
	}
}

func TestNodeRuntimeRemoveUserCallsXrayAndReportsSuccessOnlyAfterRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var gotTag, gotEmail string
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
		if err := stream.RecvMsg(request); err != nil {
			return err
		}
		gotTag = request.Get(request.Descriptor().Fields().ByNumber(1)).String()
		typed := request.Get(request.Descriptor().Fields().ByNumber(2)).Message()
		if got := typed.Get(typed.Descriptor().Fields().ByNumber(1)).String(); got != "xray.app.proxyman.command.RemoveUserOperation" {
			return status.Errorf(codes.InvalidArgument, "unexpected operation type %q", got)
		}
		operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("RemoveUserOperation"))
		if err := proto.Unmarshal(typed.Get(typed.Descriptor().Fields().ByNumber(2)).Bytes(), operation); err != nil {
			return err
		}
		gotEmail = operation.Get(operation.Descriptor().Fields().ByNumber(1)).String()
		return stream.SendMsg(dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse")))
	}))
	go server.Serve(listener)
	t.Cleanup(server.Stop)

	s := New(Config{XrayPath: "xray-test-does-not-exist", XrayAPIPort: listener.Addr().(*net.TCPAddr).Port})
	response, err := s.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{
		OperationId: "remove-1",
		InboundTag:  "vless-main",
		Email:       "user@example.test",
	})
	if err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	if !response.GetAccepted() || response.GetOperationId() != "remove-1" || gotTag != "vless-main" || gotEmail != "user@example.test" {
		t.Fatalf("response=%+v tag=%q email=%q", response, gotTag, gotEmail)
	}
}

func TestNodeRuntimeUserOperationsDoNotReportUnsupportedMutationsAsAccepted(t *testing.T) {
	s := New(Config{})
	unsupported := &nodev1.InboundUserRequest{InboundTag: "inbound", User: &nodev1.InboundUser{Protocol: "pptp"}}
	if response, err := s.AddUser(context.Background(), unsupported); response != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("AddUser response=%v err=%v, want unimplemented", response, err)
	}
	if response, err := s.UpdateUser(context.Background(), unsupported); response != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("UpdateUser response=%v err=%v, want unimplemented", response, err)
	}
	malformedOpenVPN := &nodev1.InboundUserRequest{InboundTag: "inbound", User: &nodev1.InboundUser{Protocol: "openvpn"}}
	if response, err := s.AddUser(context.Background(), malformedOpenVPN); response != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed OpenVPN AddUser response=%v err=%v, want invalid argument", response, err)
	}
}

func TestRuntimeStateAdvertisesOnlyImplementedProtocolMutationCapabilities(t *testing.T) {
	state := New(Config{}).runtimeState("test")
	seen := map[string]bool{}
	for _, capability := range state.GetCapabilities() {
		if capability == "targeted_user_update" {
			t.Fatal("generic targeted user capability overstates protocol support")
		}
		seen[capability] = true
	}
	for _, capability := range []string{"targeted_user_update_vless", "targeted_user_add_remove_vless"} {
		if !seen[capability] {
			t.Fatalf("%s capability missing although its Xray VLESS RPC is implemented", capability)
		}
	}
	for _, capability := range []string{
		"targeted_user_update_wireguard", "targeted_user_add_remove_wireguard",
		"targeted_user_update_openvpn", "targeted_user_add_remove_openvpn",
	} {
		if got, want := seen[capability], runtime.GOOS == "linux"; got != want {
			t.Fatalf("%s advertised=%v, want %v for platform %s", capability, got, want, runtime.GOOS)
		}
	}
}

func TestXrayVLESSUserOperationsAreIdempotentAndRollbackFailedUpdate(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var existing *dynamicpb.Message
	var adds, removes int
	failNextAdd := false
	var stateMu sync.Mutex
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if strings.HasSuffix(method, "/GetInboundUsers") {
			request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserRequest"))
			if err := stream.RecvMsg(request); err != nil {
				return err
			}
			stateMu.Lock()
			defer stateMu.Unlock()
			response := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserResponse"))
			if existing != nil && existing.Get(existing.Descriptor().Fields().ByNumber(2)).String() == request.Get(request.Descriptor().Fields().ByNumber(2)).String() {
				response.Mutable(response.Descriptor().Fields().ByNumber(1)).List().Append(protoreflect.ValueOfMessage(existing))
			}
			return stream.SendMsg(response)
		}
		request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
		if err := stream.RecvMsg(request); err != nil {
			return err
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		typed := request.Get(request.Descriptor().Fields().ByNumber(2)).Message()
		typeName := typed.Get(typed.Descriptor().Fields().ByNumber(1)).String()
		raw := typed.Get(typed.Descriptor().Fields().ByNumber(2)).Bytes()
		switch typeName {
		case "xray.app.proxyman.command.RemoveUserOperation":
			operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("RemoveUserOperation"))
			if err := proto.Unmarshal(raw, operation); err != nil {
				return err
			}
			if existing != nil && existing.Get(existing.Descriptor().Fields().ByNumber(2)).String() == operation.Get(operation.Descriptor().Fields().ByNumber(1)).String() {
				existing = nil
			}
			removes++
		case "xray.app.proxyman.command.AddUserOperation":
			operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AddUserOperation"))
			if err := proto.Unmarshal(raw, operation); err != nil {
				return err
			}
			if failNextAdd {
				failNextAdd = false
				return status.Error(codes.Unavailable, "simulated add failure")
			}
			user := operation.Get(operation.Descriptor().Fields().ByNumber(1)).Message()
			existing = proto.Clone(user.Interface()).(*dynamicpb.Message)
			adds++
		default:
			return status.Errorf(codes.InvalidArgument, "unexpected operation type %q", typeName)
		}
		return stream.SendMsg(dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse")))
	}))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	client := newXrayStatsClient("unused", listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(client.rpc.close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	user := &nodev1.InboundUser{Protocol: "vless", Email: "user@example.test", Fields: map[string]string{"id": "00000000-0000-4000-8000-000000000001", "level": "0"}}
	message, err := xrayVLESSUserMessage(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ensureVLESSUserRPC(ctx, "vless-main", message, false); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := client.ensureVLESSUserRPC(ctx, "vless-main", message, false); err != nil {
		t.Fatalf("idempotent add: %v", err)
	}
	stateMu.Lock()
	addCount := adds
	stateMu.Unlock()
	if addCount != 1 {
		t.Fatalf("idempotent add calls=%d", addCount)
	}

	user.Fields["id"] = "00000000-0000-4000-8000-000000000002"
	updated, err := xrayVLESSUserMessage(user)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ensureVLESSUserRPC(ctx, "vless-main", updated, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	stateMu.Lock()
	addCount, removeCount := adds, removes
	stateMu.Unlock()
	if addCount != 2 || removeCount != 1 {
		t.Fatalf("update adds=%d removes=%d", addCount, removeCount)
	}

	user.Fields["id"] = "00000000-0000-4000-8000-000000000003"
	failedUpdate, err := xrayVLESSUserMessage(user)
	if err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	failNextAdd = true
	stateMu.Unlock()
	if err := client.ensureVLESSUserRPC(ctx, "vless-main", failedUpdate, true); status.Code(err) != codes.Unavailable {
		t.Fatalf("failed update error=%v", err)
	}
	stateMu.Lock()
	addCount = adds
	retained := proto.Clone(existing.Interface()).(*dynamicpb.Message)
	stateMu.Unlock()
	if addCount != 3 {
		t.Fatalf("failed update rollback add calls=%d", addCount)
	}
	account := retained.Get(retained.Descriptor().Fields().ByNumber(3)).Message()
	accountTyped := account.Get(account.Descriptor().Fields().ByNumber(2)).Bytes()
	decodedAccount := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("VlessAccount"))
	if err := proto.Unmarshal(accountTyped, decodedAccount); err != nil {
		t.Fatal(err)
	}
	if got := decodedAccount.Get(decodedAccount.Descriptor().Fields().ByNumber(1)).String(); got != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("rollback retained VLESS id=%q", got)
	}
}
