package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var errUnsupportedXrayUserProtocol = errors.New("targeted Xray user operation supports VLESS only")

// TypedMessage embeds the registered operation name and its protobuf payload.
// Field numbers match Xray's proxyman command and common/serial protocols.
var xrayUserWireDescriptor = func() protoreflect.FileDescriptor {
	field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: &kind, Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
	}
	messageField := func(name string, number int32, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
		label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		if repeated {
			label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		}
		f := field(name, number, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
		f.Label = label.Enum()
		f.TypeName = proto.String(typeName)
		return f
	}
	operation := field("operation", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	operation.TypeName = proto.String(".antimage.xray.wire.TypedMessage")
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("antimage/xray/user_subset.proto"), Package: proto.String("antimage.xray.wire"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("RemoveUserOperation"), Field: []*descriptorpb.FieldDescriptorProto{field("email", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("AddUserOperation"), Field: []*descriptorpb.FieldDescriptorProto{messageField("user", 1, ".antimage.xray.wire.User", false)}},
			{Name: proto.String("TypedMessage"), Field: []*descriptorpb.FieldDescriptorProto{field("type", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_BYTES)}},
			{Name: proto.String("User"), Field: []*descriptorpb.FieldDescriptorProto{field("level", 1, descriptorpb.FieldDescriptorProto_TYPE_UINT32), field("email", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING), messageField("account", 3, ".antimage.xray.wire.TypedMessage", false)}},
			{Name: proto.String("VlessAccount"), Field: []*descriptorpb.FieldDescriptorProto{
				field("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("flow", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("encryption", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("xor_mode", 4, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("seconds", 5, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("padding", 6, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			}},
			{Name: proto.String("GetInboundUserRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("email", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}},
			{Name: proto.String("GetInboundUserResponse"), Field: []*descriptorpb.FieldDescriptorProto{messageField("users", 1, ".antimage.xray.wire.User", true)}},
			{Name: proto.String("AlterInboundRequest"), Field: []*descriptorpb.FieldDescriptorProto{field("tag", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), operation}},
			{Name: proto.String("AlterInboundResponse")},
		},
	}, nil)
	if err != nil {
		panic(err)
	}
	return file
}()

func xrayVLESSUserMessage(user *nodev1.InboundUser) (*dynamicpb.Message, error) {
	if user == nil || !strings.EqualFold(strings.TrimSpace(user.GetProtocol()), "vless") {
		return nil, errUnsupportedXrayUserProtocol
	}
	id := strings.TrimSpace(user.GetFields()["id"])
	email := strings.TrimSpace(user.GetEmail())
	if id == "" || email == "" {
		return nil, fmt.Errorf("VLESS user id and email are required")
	}
	level := uint64(0)
	if value := strings.TrimSpace(user.GetFields()["level"]); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid VLESS user level: %w", err)
		}
		level = parsed
	}
	account := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("VlessAccount"))
	account.Set(account.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(id))
	if flow := strings.TrimSpace(user.GetFields()["flow"]); flow != "" {
		account.Set(account.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfString(flow))
	}
	if encryption := strings.TrimSpace(user.GetFields()["encryption"]); encryption != "" {
		account.Set(account.Descriptor().Fields().ByNumber(3), protoreflect.ValueOfString(encryption))
	}
	if padding := strings.TrimSpace(user.GetFields()["padding"]); padding != "" {
		account.Set(account.Descriptor().Fields().ByNumber(6), protoreflect.ValueOfString(padding))
	}
	accountBytes, err := proto.Marshal(account)
	if err != nil {
		return nil, err
	}
	accountTyped := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("TypedMessage"))
	accountTyped.Set(accountTyped.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("xray.proxy.vless.Account"))
	accountTyped.Set(accountTyped.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfBytes(accountBytes))
	message := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("User"))
	message.Set(message.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfUint32(uint32(level)))
	message.Set(message.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfString(email))
	message.Set(message.Descriptor().Fields().ByNumber(3), protoreflect.ValueOfMessage(accountTyped))
	return message, nil
}

func (c *xrayStatsClient) getInboundUserRPC(ctx context.Context, tag, email string) (*dynamicpb.Message, error) {
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
	users := response.Get(response.Descriptor().Fields().ByNumber(1)).List()
	for i := 0; i < users.Len(); i++ {
		user := users.Get(i).Message()
		if user.Get(user.Descriptor().Fields().ByNumber(2)).String() == email {
			return proto.Clone(user.Interface()).(*dynamicpb.Message), nil
		}
	}
	return nil, nil
}

func (c *xrayStatsClient) addUserRPC(ctx context.Context, tag string, user *dynamicpb.Message) error {
	operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AddUserOperation"))
	operation.Set(operation.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfMessage(user))
	raw, err := proto.Marshal(operation)
	if err != nil {
		return err
	}
	return c.alterInboundRPC(ctx, tag, "xray.app.proxyman.command.AddUserOperation", raw)
}

func (c *xrayStatsClient) alterInboundRPC(ctx context.Context, tag, operationType string, raw []byte) error {
	conn, err := c.rpc.client()
	if err != nil {
		return err
	}
	typed := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("TypedMessage"))
	typed.Set(typed.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(operationType))
	typed.Set(typed.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfBytes(raw))
	request := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundRequest"))
	request.Set(request.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(tag))
	request.Set(request.Descriptor().Fields().ByNumber(2), protoreflect.ValueOfMessage(typed))
	response := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("AlterInboundResponse"))
	return conn.Invoke(ctx, "/xray.app.proxyman.command.HandlerService/AlterInbound", request, response)
}

func (c *xrayStatsClient) ensureVLESSUserRPC(ctx context.Context, tag string, user *dynamicpb.Message, update bool) error {
	email := user.Get(user.Descriptor().Fields().ByNumber(2)).String()
	existing, err := c.getInboundUserRPC(ctx, tag, email)
	if err != nil {
		return err
	}
	if existing == nil {
		return c.addUserRPC(ctx, tag, user)
	}
	if proto.Equal(existing, user) {
		return nil
	}
	if !update {
		return fmt.Errorf("VLESS user %q already exists with different credentials", email)
	}
	if err := c.removeUserRPC(ctx, tag, email); err != nil {
		return err
	}
	if err := c.addUserRPC(ctx, tag, user); err != nil {
		if rollbackErr := c.addUserRPC(ctx, tag, existing); rollbackErr != nil {
			return fmt.Errorf("add updated user: %v; restore previous user: %w", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (c *xrayStatsClient) removeUserRPC(ctx context.Context, tag, email string) error {
	operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("RemoveUserOperation"))
	operation.Set(operation.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(email))
	raw, err := proto.Marshal(operation)
	if err != nil {
		return err
	}
	return c.alterInboundRPC(ctx, tag, "xray.app.proxyman.command.RemoveUserOperation", raw)
}
