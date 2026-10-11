package nodeagent

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Wire-compatible subset of Xray's public StatsService protocol:
// https://github.com/XTLS/Xray-core/blob/main/app/stats/command/command.proto
var xrayStatsWireDescriptor = func() protoreflect.FileDescriptor {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	stringType, boolType := descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_BOOL
	int64Type, messageType := descriptorpb.FieldDescriptorProto_TYPE_INT64, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: &kind, Label: &label}
	}
	stats := field("stat", 1, messageType, repeated)
	stats.TypeName = proto.String(".xray.app.stats.command.Stat")
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("antimage/xray/stats_subset.proto"), Package: proto.String("xray.app.stats.command"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("QueryStatsRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("pattern", 1, stringType, optional), field("reset", 2, boolType, optional)}},
			{Name: proto.String("Stat"), Field: []*descriptorpb.FieldDescriptorProto{field("name", 1, stringType, optional), field("value", 2, int64Type, optional)}},
			{Name: proto.String("QueryStatsResponse"), Field: []*descriptorpb.FieldDescriptorProto{stats}},
		},
	}, nil)
	if err != nil {
		panic(err)
	}
	return file
}()

type xrayRPCConnection struct {
	mu   sync.Mutex
	conn *grpc.ClientConn
	port int
}

func (c *xrayRPCConnection) client() (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", c.port), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		c.conn = conn
	}
	return c.conn, nil
}

func (c *xrayRPCConnection) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *xrayStatsClient) queryStatsRPC(ctx context.Context, pattern string, reset bool) ([]xrayStat, error) {
	conn, err := c.rpc.client()
	if err != nil {
		return nil, err
	}
	request := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsRequest"))
	request.Set(request.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(pattern))
	request.Set(request.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfBool(reset))
	response := dynamicpb.NewMessage(xrayStatsWireDescriptor.Messages().ByName("QueryStatsResponse"))
	if err := conn.Invoke(ctx, "/xray.app.stats.command.StatsService/QueryStats", request, response); err != nil {
		return nil, err
	}
	list := response.Get(response.Descriptor().Fields().ByNumber(1)).List()
	result := make([]xrayStat, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		stat := list.Get(i).Message()
		result = append(result, xrayStat{Name: stat.Get(stat.Descriptor().Fields().ByNumber(1)).String(), Value: stat.Get(stat.Descriptor().Fields().ByNumber(2)).Int()})
	}
	return result, nil
}

func (s *Server) cachedXrayStatsClient(path string, port int) *xrayStatsClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.xrayStatsClient == nil || s.xrayStatsClient.apiPort != port {
		if s.xrayStatsClient != nil {
			s.xrayStatsClient.rpc.close()
		}
		s.xrayStatsClient = newXrayStatsClient(path, port)
	}
	return s.xrayStatsClient
}

func (s *Server) closeXrayStatsClient() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.xrayStatsClient != nil {
		s.xrayStatsClient.rpc.close()
	}
}
