package nodeagent

import (
	"context"
	"net"
	"strings"
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
	if response, err := s.AddUser(context.Background(), &nodev1.InboundUserRequest{}); response != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("AddUser response=%v err=%v, want unimplemented", response, err)
	}
	if response, err := s.UpdateUser(context.Background(), &nodev1.InboundUserRequest{}); response != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("UpdateUser response=%v err=%v, want unimplemented", response, err)
	}
}
