package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var errUnsupportedXrayUserProtocol = errors.New("targeted Xray user operation does not support this protocol")

// Type URLs of the per-protocol account messages the running core resolves
// through its proto registry. Each is `package` + `message` of the core's own
// .proto, so a rename is loud (the registry rejects the name and the RPC
// fails), while a RENUMBERED field is silent: protobuf files an unknown field
// number under unknownFields and the account is accepted holding an empty
// credential. The field numbers below are pinned against xray-core
// v1.260327.0, the version this repository builds the panel against. They are
// guarded by TestXrayUserAccountFieldNumbersArePinned, and the pinned release is
// the real authority: TestXrayNonVLESSNativeTargetedMutationE2E proves every
// protocol below end-to-end against that exact binary, so a core bump that
// renumbers a field fails the native gate instead of silently shipping.
const (
	xrayVmessAccountType           = "xray.proxy.vmess.Account"
	xrayVlessAccountType           = "xray.proxy.vless.Account"
	xrayTrojanAccountType          = "xray.proxy.trojan.Account"
	xrayShadowsocksAccountType     = "xray.proxy.shadowsocks.Account"
	xrayShadowsocks2022AccountType = "xray.proxy.shadowsocks_2022.Account"
	xrayHysteriaAccountType        = "xray.proxy.hysteria.account.Account"
)

// CipherType values of xray.proxy.shadowsocks.CipherType, matching the core's
// own cipherFromString JSON mapping. UNKNOWN(0) and NONE(9) are deliberately
// absent: neither is a usable per-user Shadowsocks AEAD method.
const (
	xrayCipherAES128GCM         = 5
	xrayCipherAES256GCM         = 6
	xrayCipherChaCha20Poly1305  = 7
	xrayCipherXChaCha20Poly1305 = 8
)

// xrayShadowsocksCipherType mirrors xray-core's infra/conf cipherFromString so a
// targeted user carries exactly the method the full-sync config would build.
func xrayShadowsocksCipherType(method string) (uint32, bool) {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "aes-128-gcm", "aead_aes_128_gcm":
		return xrayCipherAES128GCM, true
	case "aes-256-gcm", "aead_aes_256_gcm":
		return xrayCipherAES256GCM, true
	case "chacha20-poly1305", "aead_chacha20_poly1305", "chacha20-ietf-poly1305":
		return xrayCipherChaCha20Poly1305, true
	case "xchacha20-poly1305", "aead_xchacha20_poly1305", "xchacha20-ietf-poly1305":
		return xrayCipherXChaCha20Poly1305, true
	}
	return 0, false
}

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
			// Field numbers below mirror the pinned core's own account protos
			// (proxy/vmess/account.proto, proxy/trojan/config.proto,
			// proxy/shadowsocks/config.proto, proxy/shadowsocks_2022/config.proto,
			// proxy/hysteria/account/config.proto). See the account type URL
			// constants above for why a renumbering here is silent.
			{Name: proto.String("VmessAccount"), Field: []*descriptorpb.FieldDescriptorProto{
				field("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			}},
			{Name: proto.String("TrojanAccount"), Field: []*descriptorpb.FieldDescriptorProto{
				field("password", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			}},
			{Name: proto.String("ShadowsocksAccount"), Field: []*descriptorpb.FieldDescriptorProto{
				field("password", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("cipher_type", 2, descriptorpb.FieldDescriptorProto_TYPE_UINT32),
				field("iv_check", 3, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
			}},
			{Name: proto.String("Shadowsocks2022Account"), Field: []*descriptorpb.FieldDescriptorProto{
				field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			}},
			{Name: proto.String("HysteriaAccount"), Field: []*descriptorpb.FieldDescriptorProto{
				field("auth", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
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

// xrayUserMessage encodes an InboundUser as the core's `common.protocol.User`
// for whichever supported protocol it carries. Every branch writes the account
// message the pinned core resolves through its proto registry (see the
// xray*Type constants), so full-sync and targeted mutation produce byte-equal
// accounts for the same user.
func xrayUserMessage(user *nodev1.InboundUser) (*dynamicpb.Message, error) {
	if user == nil {
		return nil, errUnsupportedXrayUserProtocol
	}
	protocol := strings.ToLower(strings.TrimSpace(user.GetProtocol()))
	fields := user.GetFields()
	email := strings.TrimSpace(user.GetEmail())
	if email == "" {
		return nil, fmt.Errorf("Xray user email is required")
	}

	level := uint64(0)
	if value := strings.TrimSpace(fields["level"]); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid %s user level: %w", protocol, err)
		}
		level = parsed
	}

	var accountName, accountType string
	switch protocol {
	case "vless":
		id := strings.TrimSpace(fields["id"])
		if id == "" {
			return nil, fmt.Errorf("VLESS user id is required")
		}
		accountName, accountType = "VlessAccount", xrayVlessAccountType
	case "vmess":
		id := strings.TrimSpace(fields["id"])
		if id == "" {
			return nil, fmt.Errorf("VMess user id is required")
		}
		accountName, accountType = "VmessAccount", xrayVmessAccountType
	case "trojan":
		if strings.TrimSpace(fields["password"]) == "" {
			return nil, fmt.Errorf("Trojan user password is required")
		}
		accountName, accountType = "TrojanAccount", xrayTrojanAccountType
	case "shadowsocks":
		if strings.TrimSpace(fields["password"]) == "" {
			return nil, fmt.Errorf("Shadowsocks user password is required")
		}
		// The controller strips `method` for the 2022 multi-user ciphers
		// (RuntimeShadowsocksSettings) because those accounts carry a base64 key
		// and resolve as xray.proxy.shadowsocks_2022.Account instead.
		if strings.TrimSpace(fields["method"]) == "" {
			accountName, accountType = "Shadowsocks2022Account", xrayShadowsocks2022AccountType
		} else {
			accountName, accountType = "ShadowsocksAccount", xrayShadowsocksAccountType
		}
	case "hysteria", "hysteria2":
		if strings.TrimSpace(fields["auth"]) == "" {
			return nil, fmt.Errorf("Hysteria user auth is required")
		}
		accountName, accountType = "HysteriaAccount", xrayHysteriaAccountType
	default:
		return nil, errUnsupportedXrayUserProtocol
	}

	account := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName(protoreflect.Name(accountName)))
	set := func(name string, value protoreflect.Value) {
		account.Set(account.Descriptor().Fields().ByName(protoreflect.Name(name)), value)
	}
	switch accountName {
	case "VlessAccount":
		set("id", protoreflect.ValueOfString(strings.TrimSpace(fields["id"])))
		if flow := strings.TrimSpace(fields["flow"]); flow != "" {
			set("flow", protoreflect.ValueOfString(flow))
		}
		if encryption := strings.TrimSpace(fields["encryption"]); encryption != "" {
			set("encryption", protoreflect.ValueOfString(encryption))
		}
		if padding := strings.TrimSpace(fields["padding"]); padding != "" {
			set("padding", protoreflect.ValueOfString(padding))
		}
	case "VmessAccount":
		set("id", protoreflect.ValueOfString(strings.TrimSpace(fields["id"])))
	case "TrojanAccount":
		set("password", protoreflect.ValueOfString(strings.TrimSpace(fields["password"])))
	case "ShadowsocksAccount":
		cipherType, ok := xrayShadowsocksCipherType(fields["method"])
		if !ok {
			return nil, fmt.Errorf("unsupported Shadowsocks user cipher %q", fields["method"])
		}
		set("password", protoreflect.ValueOfString(strings.TrimSpace(fields["password"])))
		set("cipher_type", protoreflect.ValueOfUint32(cipherType))
		if ivCheck, err := strconv.ParseBool(strings.TrimSpace(fields["iv_check"])); err == nil {
			set("iv_check", protoreflect.ValueOfBool(ivCheck))
		}
	case "Shadowsocks2022Account":
		set("key", protoreflect.ValueOfString(strings.TrimSpace(fields["password"])))
	case "HysteriaAccount":
		set("auth", protoreflect.ValueOfString(strings.TrimSpace(fields["auth"])))
	}

	accountBytes, err := proto.Marshal(account)
	if err != nil {
		return nil, err
	}
	accountTyped := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("TypedMessage"))
	accountTyped.Set(accountTyped.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(accountType))
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

func (c *xrayStatsClient) ensureXrayUserRPC(ctx context.Context, tag string, user *dynamicpb.Message, update bool) error {
	email := user.Get(user.Descriptor().Fields().ByNumber(2)).String()
	existing, err := c.getInboundUserRPC(ctx, tag, email)
	if err != nil {
		return err
	}
	if existing == nil {
		return c.addUserRPC(ctx, tag, user)
	}
	if xrayUserContains(existing, user) {
		return nil
	}
	if !update {
		return fmt.Errorf("Xray user %q already exists with different credentials", email)
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

// xrayUserContains reports whether every field this node sets is already
// present with the same value in the running core's stored user.
//
// It deliberately is not proto.Equal: the pinned core normalizes the accounts
// it accepts by filling in protocol defaults it owns (for example VMess
// rewrites an empty security_settings to the "auto" default). Those added
// fields are not something this node controls, so an identical retry of the
// same AddUser compares equal here while a genuinely different credential
// under the same email still differs and is refused.
func xrayUserContains(stored, desired protoreflect.Message) bool {
	storedFields, desiredFields := stored.Descriptor().Fields(), desired.Descriptor().Fields()
	for i := 0; i < desiredFields.Len(); i++ {
		field := desiredFields.Get(i)
		if !desired.Has(field) {
			continue
		}
		storedField := storedFields.ByNumber(field.Number())
		if storedField == nil || !stored.Has(storedField) {
			return false
		}
		switch {
		case field.IsMap():
			if !xrayMapContains(field.MapValue().Kind(), stored.Get(storedField).Map(), desired.Get(field).Map()) {
				return false
			}
		case field.IsList():
			if !xrayListContains(field.Kind(), stored.Get(storedField).List(), desired.Get(field).List()) {
				return false
			}
		case field.Message() != nil:
			if !xrayNestedMessageContains(stored.Get(storedField).Message(), desired.Get(field).Message()) {
				return false
			}
		default:
			if !xrayScalarEqual(field.Kind(), stored.Get(storedField), desired.Get(field)) {
				return false
			}
		}
	}
	return true
}

// xrayNestedMessageContains compares a nested message. When the message is an
// account TypedMessage the inner account is decoded and compared field by
// field, because the pinned core rewrites the opaque account bytes it stores
// (it re-serializes them after applying its own defaults). Comparing the raw
// bytes would therefore report a plain retry as a credential change.
func xrayNestedMessageContains(stored, desired protoreflect.Message) bool {
	if equal, handled := xrayTypedAccountContains(stored, desired); handled {
		return equal
	}
	return xrayUserContains(stored, desired)
}

func xrayTypedAccountContains(stored, desired protoreflect.Message) (bool, bool) {
	storedFields := stored.Descriptor().Fields()
	typeField := storedFields.ByName("type")
	valueField := storedFields.ByName("value")
	if typeField == nil || valueField == nil || typeField.Kind() != protoreflect.StringKind || valueField.Kind() != protoreflect.BytesKind {
		return false, false
	}
	name := xrayAccountMessageByType(stored.Get(typeField).String())
	if name == "" {
		return false, false
	}
	storedAccount := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName(name))
	desiredAccount := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName(name))
	if proto.Unmarshal(stored.Get(valueField).Bytes(), storedAccount) != nil {
		return false, false
	}
	if proto.Unmarshal(desired.Get(valueField).Bytes(), desiredAccount) != nil {
		return false, false
	}
	return xrayUserContains(storedAccount, desiredAccount), true
}

func xrayAccountMessageByType(accountType string) protoreflect.Name {
	switch accountType {
	case xrayVlessAccountType:
		return "VlessAccount"
	case xrayVmessAccountType:
		return "VmessAccount"
	case xrayTrojanAccountType:
		return "TrojanAccount"
	case xrayShadowsocksAccountType:
		return "ShadowsocksAccount"
	case xrayShadowsocks2022AccountType:
		return "Shadowsocks2022Account"
	case xrayHysteriaAccountType:
		return "HysteriaAccount"
	}
	return ""
}

func xrayScalarEqual(kind protoreflect.Kind, stored, desired protoreflect.Value) bool {
	if kind == protoreflect.BytesKind {
		return bytes.Equal(stored.Bytes(), desired.Bytes())
	}
	if kind == protoreflect.MessageKind || kind == protoreflect.GroupKind {
		return proto.Equal(stored.Message().Interface(), desired.Message().Interface())
	}
	return stored.Interface() == desired.Interface()
}

func xrayListContains(kind protoreflect.Kind, stored, desired protoreflect.List) bool {
	if stored.Len() != desired.Len() {
		return false
	}
	for i := 0; i < desired.Len(); i++ {
		if !xrayScalarEqual(kind, stored.Get(i), desired.Get(i)) {
			return false
		}
	}
	return true
}

func xrayMapContains(kind protoreflect.Kind, stored, desired protoreflect.Map) bool {
	equal := true
	desired.Range(func(key protoreflect.MapKey, value protoreflect.Value) bool {
		if !stored.Has(key) || !xrayScalarEqual(kind, stored.Get(key), value) {
			equal = false
			return false
		}
		return true
	})
	return equal
}

func (c *xrayStatsClient) removeUserRPC(ctx context.Context, tag, email string) error {
	operation := dynamicpb.NewMessage(xrayUserWireDescriptor.Messages().ByName("RemoveUserOperation"))
	operation.Set(operation.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString(email))
	raw, err := proto.Marshal(operation)
	if err != nil {
		return err
	}
	err = c.alterInboundRPC(ctx, tag, "xray.app.proxyman.command.RemoveUserOperation", raw)
	if err == nil {
		return nil
	}
	// The core reports a missing user as a generic error, so a retry of an
	// already-applied removal would surface as a failure. Re-read the inbound
	// to distinguish that from a genuine removal error: a user that is gone is
	// the desired end state, while any other error is propagated untouched.
	if status.Code(err) == codes.PermissionDenied {
		return err
	}
	missing, readErr := c.getInboundUserRPC(ctx, tag, email)
	if readErr == nil && missing == nil {
		return nil
	}
	return err
}
