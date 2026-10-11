package nodeagent

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// TypedMessage embeds the registered operation name and its protobuf payload.
// Field numbers match Xray's proxyman command and common/serial protocols.
var xrayUserWireDescriptor = func() protoreflect.FileDescriptor {
	field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: &kind, Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
	}
	operation := field("operation", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	operation.TypeName = proto.String(".antimage.xray.wire.TypedMessage")
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("antimage/xray/user_subset.proto"), Package: proto.String("antimage.xray.wire"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("RemoveUserOperation"), Field: []*descriptorpb.FieldDescriptorProto{field("email", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("TypedMessage"), Field: []*descriptorpb.FieldDescriptorProto{field("type", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_BYTES)}},
			{Name: proto.String("AlterInboundRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), operation}},
			{Name: proto.String("AlterInboundResponse")},
		},
	}, nil)
	if err != nil {
		panic(err)
	}
	return file
}()

func (c *xrayStatsClient) removeUserRPC(ctx context.Context, tag, email string) error {
	conn, err := c.rpc.client()
	if err != nil {
		return err
	}
	operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("RemoveUserOperation"))
	operation.Set(operation.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(email))
	raw, err := proto.Marshal(operation)
	if err != nil {
		return err
	}
	typed := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("TypedMessage"))
	typed.Set(typed.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("xray.app.proxyman.command.RemoveUserOperation"))
	typed.Set(typed.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfBytes(raw))
	request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
	request.Set(request.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(tag))
	request.Set(request.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfMessage(typed))
	response := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse"))
	return conn.Invoke(ctx, "/xray.app.proxyman.command.HandlerService/AlterInbound", request, response)
}
