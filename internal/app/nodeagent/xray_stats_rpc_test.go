package nodeagent

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
