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
	account := field("account", 3, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	account.TypeName = proto.String(".antimage.xray.wire.TypedMessage")
	user := field("user", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	user.TypeName = proto.String(".antimage.xray.wire.User")
	users := field("users", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	users.TypeName = proto.String(".antimage.xray.wire.User")
	users.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("antimage/xray/user_subset.proto"), Package: proto.String("antimage.xray.wire"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("User"), Field: []*descriptorpb.FieldDescriptorProto{field("level", 1, descriptorpb.FieldDescriptorProto_TYPE_UINT32), field("email", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING), account}},
			{Name: proto.String("AddUserOperation"), Field: []*descriptorpb.FieldDescriptorProto{user}},
			{Name: proto.String("GetInboundUserRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("email", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("GetInboundUserResponse"), Field: []*descriptorpb.FieldDescriptorProto{users}},
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

func xrayTypedWireMessage(kind string, raw []byte) *dynamicpb.Message {
	typed := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("TypedMessage"))
	typed.Set(typed.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(kind))
	typed.Set(typed.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfBytes(raw))
	return typed
}

func (c *xrayStatsClient) addUserRPC(ctx context.Context, tag string, user xrayLiveUser) error {
	conn, err := c.rpc.client()
	if err != nil {
		return err
	}
	message := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("User"))
	message.Set(message.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfUint32(user.Level))
	message.Set(message.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfString(user.Email))
	message.Set(message.Descriptor().Fields().ByNumber(3), protoreflect.ValueOfMessage(xrayTypedWireMessage(user.AccountType, user.Account)))
	operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AddUserOperation"))
	operation.Set(operation.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfMessage(message))
	raw, err := proto.Marshal(operation)
	if err != nil {
		return err
	}
	request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
	request.Set(request.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(tag))
	request.Set(request.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfMessage(xrayTypedWireMessage("xray.app.proxyman.command.AddUserOperation", raw)))
	return conn.Invoke(ctx, "/xray.app.proxyman.command.HandlerService/AlterInbound", request, dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse")))
}

func (c *xrayStatsClient) getUserRPC(ctx context.Context, tag, email string) (*xrayLiveUser, error) {
	conn, err := c.rpc.client()
	if err != nil {
		return nil, err
	}
	request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserRequest"))
	request.Set(request.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(tag))
	request.Set(request.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfString(email))
	response := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("GetInboundUserResponse"))
	if err := conn.Invoke(ctx, "/xray.app.proxyman.command.HandlerService/GetInboundUsers", request, response); err != nil {
		return nil, err
	}
	list := response.Get(response.Descriptor().Fields().ByNumber(1)).List()
	for i := 0; i < list.Len(); i++ {
		user := list.Get(i).Message()
		if user.Get(user.Descriptor().Fields().ByNumber(2)).String() == "" {
			continue
		}
		account := user.Get(user.Descriptor().Fields().ByNumber(3)).Message()
		return &xrayLiveUser{Email: user.Get(user.Descriptor().Fields().ByNumber(2)).String(), Level: uint32(user.Get(user.Descriptor().Fields().ByNumber(1)).Uint()), AccountType: account.Get(account.Descriptor().Fields().ByNumber(1)).String(), Account: append([]byte(nil), account.Get(account.Descriptor().Fields().ByNumber(2)).Bytes()...)}, nil
	}
	return nil, nil
}

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
