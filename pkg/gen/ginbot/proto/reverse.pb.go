// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.1
// 	protoc        v5.29.2
// source: ginbot/proto/reverse.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	_ "google.golang.org/protobuf/types/gofeaturespb"
	structpb "google.golang.org/protobuf/types/known/structpb"
	reflect "reflect"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ClientAction int32

const (
	ClientAction_CLIENT_ACTION_UNSPECIFIED              ClientAction = 0
	ClientAction_CLIENT_ACTION_SEND_NOTIFICATION        ClientAction = 1
	ClientAction_CLIENT_ACTION_SEND_NOTIFICATION_URGENT ClientAction = 2
	ClientAction_CLIENT_ACTION_SEND_TEST                ClientAction = 999
)

// Enum value maps for ClientAction.
var (
	ClientAction_name = map[int32]string{
		0:   "CLIENT_ACTION_UNSPECIFIED",
		1:   "CLIENT_ACTION_SEND_NOTIFICATION",
		2:   "CLIENT_ACTION_SEND_NOTIFICATION_URGENT",
		999: "CLIENT_ACTION_SEND_TEST",
	}
	ClientAction_value = map[string]int32{
		"CLIENT_ACTION_UNSPECIFIED":              0,
		"CLIENT_ACTION_SEND_NOTIFICATION":        1,
		"CLIENT_ACTION_SEND_NOTIFICATION_URGENT": 2,
		"CLIENT_ACTION_SEND_TEST":                999,
	}
)

func (x ClientAction) Enum() *ClientAction {
	p := new(ClientAction)
	*p = x
	return p
}

func (x ClientAction) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ClientAction) Descriptor() protoreflect.EnumDescriptor {
	return file_ginbot_proto_reverse_proto_enumTypes[0].Descriptor()
}

func (ClientAction) Type() protoreflect.EnumType {
	return &file_ginbot_proto_reverse_proto_enumTypes[0]
}

func (x ClientAction) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

type OpenClientActionStreamReq struct {
	state                   protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_PlatformEnum Platform               `protobuf:"varint,1,opt,name=platform_enum,json=platformEnum,enum=ginbot.proto.Platform" json:"platform_enum,omitempty"`
	XXX_raceDetectHookData  protoimpl.RaceDetectHookData
	XXX_presence            [1]uint32
	unknownFields           protoimpl.UnknownFields
	sizeCache               protoimpl.SizeCache
}

func (x *OpenClientActionStreamReq) Reset() {
	*x = OpenClientActionStreamReq{}
	mi := &file_ginbot_proto_reverse_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OpenClientActionStreamReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OpenClientActionStreamReq) ProtoMessage() {}

func (x *OpenClientActionStreamReq) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_reverse_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *OpenClientActionStreamReq) GetPlatformEnum() Platform {
	if x != nil {
		if protoimpl.X.Present(&(x.XXX_presence[0]), 0) {
			return x.xxx_hidden_PlatformEnum
		}
	}
	return Platform_PLATFORM_UNSPECIFIED
}

func (x *OpenClientActionStreamReq) SetPlatformEnum(v Platform) {
	x.xxx_hidden_PlatformEnum = v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 1)
}

func (x *OpenClientActionStreamReq) HasPlatformEnum() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *OpenClientActionStreamReq) ClearPlatformEnum() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_PlatformEnum = Platform_PLATFORM_UNSPECIFIED
}

type OpenClientActionStreamReq_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Platform of the Connection
	PlatformEnum *Platform
}

func (b0 OpenClientActionStreamReq_builder) Build() *OpenClientActionStreamReq {
	m0 := &OpenClientActionStreamReq{}
	b, x := &b0, m0
	_, _ = b, x
	if b.PlatformEnum != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 1)
		x.xxx_hidden_PlatformEnum = *b.PlatformEnum
	}
	return m0
}

type OpenClientActionStreamResp struct {
	state                   protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_PlatformEnum Platform               `protobuf:"varint,1,opt,name=platform_enum,json=platformEnum,enum=ginbot.proto.Platform" json:"platform_enum,omitempty"`
	xxx_hidden_ClientAction ClientAction           `protobuf:"varint,2,opt,name=client_action,json=clientAction,enum=ginbot.proto.ClientAction" json:"client_action,omitempty"`
	xxx_hidden_Content      *structpb.Struct       `protobuf:"bytes,3,opt,name=content" json:"content,omitempty"`
	XXX_raceDetectHookData  protoimpl.RaceDetectHookData
	XXX_presence            [1]uint32
	unknownFields           protoimpl.UnknownFields
	sizeCache               protoimpl.SizeCache
}

func (x *OpenClientActionStreamResp) Reset() {
	*x = OpenClientActionStreamResp{}
	mi := &file_ginbot_proto_reverse_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OpenClientActionStreamResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OpenClientActionStreamResp) ProtoMessage() {}

func (x *OpenClientActionStreamResp) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_reverse_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *OpenClientActionStreamResp) GetPlatformEnum() Platform {
	if x != nil {
		if protoimpl.X.Present(&(x.XXX_presence[0]), 0) {
			return x.xxx_hidden_PlatformEnum
		}
	}
	return Platform_PLATFORM_UNSPECIFIED
}

func (x *OpenClientActionStreamResp) GetClientAction() ClientAction {
	if x != nil {
		if protoimpl.X.Present(&(x.XXX_presence[0]), 1) {
			return x.xxx_hidden_ClientAction
		}
	}
	return ClientAction_CLIENT_ACTION_UNSPECIFIED
}

func (x *OpenClientActionStreamResp) GetContent() *structpb.Struct {
	if x != nil {
		return x.xxx_hidden_Content
	}
	return nil
}

func (x *OpenClientActionStreamResp) SetPlatformEnum(v Platform) {
	x.xxx_hidden_PlatformEnum = v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 3)
}

func (x *OpenClientActionStreamResp) SetClientAction(v ClientAction) {
	x.xxx_hidden_ClientAction = v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 1, 3)
}

func (x *OpenClientActionStreamResp) SetContent(v *structpb.Struct) {
	x.xxx_hidden_Content = v
}

func (x *OpenClientActionStreamResp) HasPlatformEnum() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *OpenClientActionStreamResp) HasClientAction() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 1)
}

func (x *OpenClientActionStreamResp) HasContent() bool {
	if x == nil {
		return false
	}
	return x.xxx_hidden_Content != nil
}

func (x *OpenClientActionStreamResp) ClearPlatformEnum() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_PlatformEnum = Platform_PLATFORM_UNSPECIFIED
}

func (x *OpenClientActionStreamResp) ClearClientAction() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 1)
	x.xxx_hidden_ClientAction = ClientAction_CLIENT_ACTION_UNSPECIFIED
}

func (x *OpenClientActionStreamResp) ClearContent() {
	x.xxx_hidden_Content = nil
}

type OpenClientActionStreamResp_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Platform of the Connection
	PlatformEnum *Platform
	// Action to be taken by the client
	ClientAction *ClientAction
	// Content to be sent processed by the client
	Content *structpb.Struct
}

func (b0 OpenClientActionStreamResp_builder) Build() *OpenClientActionStreamResp {
	m0 := &OpenClientActionStreamResp{}
	b, x := &b0, m0
	_, _ = b, x
	if b.PlatformEnum != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 3)
		x.xxx_hidden_PlatformEnum = *b.PlatformEnum
	}
	if b.ClientAction != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 1, 3)
		x.xxx_hidden_ClientAction = *b.ClientAction
	}
	x.xxx_hidden_Content = b.Content
	return m0
}

var File_ginbot_proto_reverse_proto protoreflect.FileDescriptor

var file_ginbot_proto_reverse_proto_rawDesc = []byte{
	0x0a, 0x1a, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x72,
	0x65, 0x76, 0x65, 0x72, 0x73, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x0c, 0x67, 0x69,
	0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1b, 0x67, 0x69, 0x6e, 0x62,
	0x6f, 0x74, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x70, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72,
	0x6d, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1c, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x73, 0x74, 0x72, 0x75, 0x63, 0x74, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x21, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x67, 0x6f, 0x5f, 0x66, 0x65, 0x61, 0x74, 0x75, 0x72,
	0x65, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x58, 0x0a, 0x19, 0x4f, 0x70, 0x65, 0x6e,
	0x43, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x53, 0x74, 0x72, 0x65,
	0x61, 0x6d, 0x52, 0x65, 0x71, 0x12, 0x3b, 0x0a, 0x0d, 0x70, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72,
	0x6d, 0x5f, 0x65, 0x6e, 0x75, 0x6d, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x16, 0x2e, 0x67,
	0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x50, 0x6c, 0x61, 0x74,
	0x66, 0x6f, 0x72, 0x6d, 0x52, 0x0c, 0x70, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72, 0x6d, 0x45, 0x6e,
	0x75, 0x6d, 0x22, 0xcd, 0x01, 0x0a, 0x1a, 0x4f, 0x70, 0x65, 0x6e, 0x43, 0x6c, 0x69, 0x65, 0x6e,
	0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x52, 0x65, 0x73,
	0x70, 0x12, 0x3b, 0x0a, 0x0d, 0x70, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72, 0x6d, 0x5f, 0x65, 0x6e,
	0x75, 0x6d, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x16, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f,
	0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x50, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72, 0x6d,
	0x52, 0x0c, 0x70, 0x6c, 0x61, 0x74, 0x66, 0x6f, 0x72, 0x6d, 0x45, 0x6e, 0x75, 0x6d, 0x12, 0x3f,
	0x0a, 0x0d, 0x63, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x5f, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1a, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x43, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f,
	0x6e, 0x52, 0x0c, 0x63, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x12,
	0x31, 0x0a, 0x07, 0x63, 0x6f, 0x6e, 0x74, 0x65, 0x6e, 0x74, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x17, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62,
	0x75, 0x66, 0x2e, 0x53, 0x74, 0x72, 0x75, 0x63, 0x74, 0x52, 0x07, 0x63, 0x6f, 0x6e, 0x74, 0x65,
	0x6e, 0x74, 0x2a, 0x9c, 0x01, 0x0a, 0x0c, 0x43, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x41, 0x63, 0x74,
	0x69, 0x6f, 0x6e, 0x12, 0x1d, 0x0a, 0x19, 0x43, 0x4c, 0x49, 0x45, 0x4e, 0x54, 0x5f, 0x41, 0x43,
	0x54, 0x49, 0x4f, 0x4e, 0x5f, 0x55, 0x4e, 0x53, 0x50, 0x45, 0x43, 0x49, 0x46, 0x49, 0x45, 0x44,
	0x10, 0x00, 0x12, 0x23, 0x0a, 0x1f, 0x43, 0x4c, 0x49, 0x45, 0x4e, 0x54, 0x5f, 0x41, 0x43, 0x54,
	0x49, 0x4f, 0x4e, 0x5f, 0x53, 0x45, 0x4e, 0x44, 0x5f, 0x4e, 0x4f, 0x54, 0x49, 0x46, 0x49, 0x43,
	0x41, 0x54, 0x49, 0x4f, 0x4e, 0x10, 0x01, 0x12, 0x2a, 0x0a, 0x26, 0x43, 0x4c, 0x49, 0x45, 0x4e,
	0x54, 0x5f, 0x41, 0x43, 0x54, 0x49, 0x4f, 0x4e, 0x5f, 0x53, 0x45, 0x4e, 0x44, 0x5f, 0x4e, 0x4f,
	0x54, 0x49, 0x46, 0x49, 0x43, 0x41, 0x54, 0x49, 0x4f, 0x4e, 0x5f, 0x55, 0x52, 0x47, 0x45, 0x4e,
	0x54, 0x10, 0x02, 0x12, 0x1c, 0x0a, 0x17, 0x43, 0x4c, 0x49, 0x45, 0x4e, 0x54, 0x5f, 0x41, 0x43,
	0x54, 0x49, 0x4f, 0x4e, 0x5f, 0x53, 0x45, 0x4e, 0x44, 0x5f, 0x54, 0x45, 0x53, 0x54, 0x10, 0xe7,
	0x07, 0x32, 0x81, 0x01, 0x0a, 0x0e, 0x52, 0x65, 0x76, 0x65, 0x72, 0x73, 0x65, 0x53, 0x65, 0x72,
	0x76, 0x69, 0x63, 0x65, 0x12, 0x6f, 0x0a, 0x16, 0x4f, 0x70, 0x65, 0x6e, 0x43, 0x6c, 0x69, 0x65,
	0x6e, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x12, 0x27,
	0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4f, 0x70,
	0x65, 0x6e, 0x43, 0x6c, 0x69, 0x65, 0x6e, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x53, 0x74,
	0x72, 0x65, 0x61, 0x6d, 0x52, 0x65, 0x71, 0x1a, 0x28, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74,
	0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x4f, 0x70, 0x65, 0x6e, 0x43, 0x6c, 0x69, 0x65, 0x6e,
	0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x52, 0x65, 0x73,
	0x70, 0x28, 0x01, 0x30, 0x01, 0x42, 0x38, 0x5a, 0x2e, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e,
	0x63, 0x6f, 0x6d, 0x2f, 0x6c, 0x61, 0x73, 0x69, 0x6b, 0x75, 0x75, 0x2f, 0x47, 0x69, 0x6e, 0x42,
	0x6f, 0x74, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x67, 0x65, 0x6e, 0x2f, 0x67, 0x69, 0x6e, 0x62, 0x6f,
	0x74, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x92, 0x03, 0x05, 0xd2, 0x3e, 0x02, 0x10, 0x03, 0x62,
	0x08, 0x65, 0x64, 0x69, 0x74, 0x69, 0x6f, 0x6e, 0x73, 0x70, 0xe8, 0x07,
}

var file_ginbot_proto_reverse_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_ginbot_proto_reverse_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_ginbot_proto_reverse_proto_goTypes = []any{
	(ClientAction)(0),                  // 0: ginbot.proto.ClientAction
	(*OpenClientActionStreamReq)(nil),  // 1: ginbot.proto.OpenClientActionStreamReq
	(*OpenClientActionStreamResp)(nil), // 2: ginbot.proto.OpenClientActionStreamResp
	(Platform)(0),                      // 3: ginbot.proto.Platform
	(*structpb.Struct)(nil),            // 4: google.protobuf.Struct
}
var file_ginbot_proto_reverse_proto_depIdxs = []int32{
	3, // 0: ginbot.proto.OpenClientActionStreamReq.platform_enum:type_name -> ginbot.proto.Platform
	3, // 1: ginbot.proto.OpenClientActionStreamResp.platform_enum:type_name -> ginbot.proto.Platform
	0, // 2: ginbot.proto.OpenClientActionStreamResp.client_action:type_name -> ginbot.proto.ClientAction
	4, // 3: ginbot.proto.OpenClientActionStreamResp.content:type_name -> google.protobuf.Struct
	1, // 4: ginbot.proto.ReverseService.OpenClientActionStream:input_type -> ginbot.proto.OpenClientActionStreamReq
	2, // 5: ginbot.proto.ReverseService.OpenClientActionStream:output_type -> ginbot.proto.OpenClientActionStreamResp
	5, // [5:6] is the sub-list for method output_type
	4, // [4:5] is the sub-list for method input_type
	4, // [4:4] is the sub-list for extension type_name
	4, // [4:4] is the sub-list for extension extendee
	0, // [0:4] is the sub-list for field type_name
}

func init() { file_ginbot_proto_reverse_proto_init() }
func file_ginbot_proto_reverse_proto_init() {
	if File_ginbot_proto_reverse_proto != nil {
		return
	}
	file_ginbot_proto_platform_proto_init()
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_ginbot_proto_reverse_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_ginbot_proto_reverse_proto_goTypes,
		DependencyIndexes: file_ginbot_proto_reverse_proto_depIdxs,
		EnumInfos:         file_ginbot_proto_reverse_proto_enumTypes,
		MessageInfos:      file_ginbot_proto_reverse_proto_msgTypes,
	}.Build()
	File_ginbot_proto_reverse_proto = out.File
	file_ginbot_proto_reverse_proto_rawDesc = nil
	file_ginbot_proto_reverse_proto_goTypes = nil
	file_ginbot_proto_reverse_proto_depIdxs = nil
}
