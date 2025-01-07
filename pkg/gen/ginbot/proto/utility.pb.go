// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.1
// 	protoc        v5.29.2
// source: ginbot/proto/utility.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	_ "google.golang.org/protobuf/types/gofeaturespb"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	reflect "reflect"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type HealthStatus int32

const (
	HealthStatus_HEALTH_STATUS_UNSPECIFIED HealthStatus = 0
	HealthStatus_HEALTH_STATUS_OK          HealthStatus = 1
	HealthStatus_HEALTH_STATUS_ERROR       HealthStatus = 2
)

// Enum value maps for HealthStatus.
var (
	HealthStatus_name = map[int32]string{
		0: "HEALTH_STATUS_UNSPECIFIED",
		1: "HEALTH_STATUS_OK",
		2: "HEALTH_STATUS_ERROR",
	}
	HealthStatus_value = map[string]int32{
		"HEALTH_STATUS_UNSPECIFIED": 0,
		"HEALTH_STATUS_OK":          1,
		"HEALTH_STATUS_ERROR":       2,
	}
)

func (x HealthStatus) Enum() *HealthStatus {
	p := new(HealthStatus)
	*p = x
	return p
}

func (x HealthStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (HealthStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_ginbot_proto_utility_proto_enumTypes[0].Descriptor()
}

func (HealthStatus) Type() protoreflect.EnumType {
	return &file_ginbot_proto_utility_proto_enumTypes[0]
}

func (x HealthStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

type HealthCheckResp struct {
	state                  protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_Status      HealthStatus           `protobuf:"varint,1,opt,name=status,enum=ginbot.proto.HealthStatus" json:"status,omitempty"`
	XXX_raceDetectHookData protoimpl.RaceDetectHookData
	XXX_presence           [1]uint32
	unknownFields          protoimpl.UnknownFields
	sizeCache              protoimpl.SizeCache
}

func (x *HealthCheckResp) Reset() {
	*x = HealthCheckResp{}
	mi := &file_ginbot_proto_utility_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *HealthCheckResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HealthCheckResp) ProtoMessage() {}

func (x *HealthCheckResp) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_utility_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *HealthCheckResp) GetStatus() HealthStatus {
	if x != nil {
		if protoimpl.X.Present(&(x.XXX_presence[0]), 0) {
			return x.xxx_hidden_Status
		}
	}
	return HealthStatus_HEALTH_STATUS_UNSPECIFIED
}

func (x *HealthCheckResp) SetStatus(v HealthStatus) {
	x.xxx_hidden_Status = v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 1)
}

func (x *HealthCheckResp) HasStatus() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *HealthCheckResp) ClearStatus() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_Status = HealthStatus_HEALTH_STATUS_UNSPECIFIED
}

type HealthCheckResp_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Status of the service
	Status *HealthStatus
}

func (b0 HealthCheckResp_builder) Build() *HealthCheckResp {
	m0 := &HealthCheckResp{}
	b, x := &b0, m0
	_, _ = b, x
	if b.Status != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 1)
		x.xxx_hidden_Status = *b.Status
	}
	return m0
}

type HelpReq struct {
	state                  protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_Command     *string                `protobuf:"bytes,1,opt,name=command" json:"command,omitempty"`
	XXX_raceDetectHookData protoimpl.RaceDetectHookData
	XXX_presence           [1]uint32
	unknownFields          protoimpl.UnknownFields
	sizeCache              protoimpl.SizeCache
}

func (x *HelpReq) Reset() {
	*x = HelpReq{}
	mi := &file_ginbot_proto_utility_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *HelpReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HelpReq) ProtoMessage() {}

func (x *HelpReq) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_utility_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *HelpReq) GetCommand() string {
	if x != nil {
		if x.xxx_hidden_Command != nil {
			return *x.xxx_hidden_Command
		}
		return ""
	}
	return ""
}

func (x *HelpReq) SetCommand(v string) {
	x.xxx_hidden_Command = &v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 1)
}

func (x *HelpReq) HasCommand() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *HelpReq) ClearCommand() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_Command = nil
}

type HelpReq_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Command to get help for
	Command *string
}

func (b0 HelpReq_builder) Build() *HelpReq {
	m0 := &HelpReq{}
	b, x := &b0, m0
	_, _ = b, x
	if b.Command != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 1)
		x.xxx_hidden_Command = b.Command
	}
	return m0
}

type HelpResp struct {
	state                  protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_Message     *string                `protobuf:"bytes,1,opt,name=message" json:"message,omitempty"`
	XXX_raceDetectHookData protoimpl.RaceDetectHookData
	XXX_presence           [1]uint32
	unknownFields          protoimpl.UnknownFields
	sizeCache              protoimpl.SizeCache
}

func (x *HelpResp) Reset() {
	*x = HelpResp{}
	mi := &file_ginbot_proto_utility_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *HelpResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HelpResp) ProtoMessage() {}

func (x *HelpResp) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_utility_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *HelpResp) GetMessage() string {
	if x != nil {
		if x.xxx_hidden_Message != nil {
			return *x.xxx_hidden_Message
		}
		return ""
	}
	return ""
}

func (x *HelpResp) SetMessage(v string) {
	x.xxx_hidden_Message = &v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 1)
}

func (x *HelpResp) HasMessage() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *HelpResp) ClearMessage() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_Message = nil
}

type HelpResp_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Help message
	Message *string
}

func (b0 HelpResp_builder) Build() *HelpResp {
	m0 := &HelpResp{}
	b, x := &b0, m0
	_, _ = b, x
	if b.Message != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 1)
		x.xxx_hidden_Message = b.Message
	}
	return m0
}

type PingResp struct {
	state                  protoimpl.MessageState `protogen:"opaque.v1"`
	xxx_hidden_Message     *string                `protobuf:"bytes,1,opt,name=message" json:"message,omitempty"`
	XXX_raceDetectHookData protoimpl.RaceDetectHookData
	XXX_presence           [1]uint32
	unknownFields          protoimpl.UnknownFields
	sizeCache              protoimpl.SizeCache
}

func (x *PingResp) Reset() {
	*x = PingResp{}
	mi := &file_ginbot_proto_utility_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *PingResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PingResp) ProtoMessage() {}

func (x *PingResp) ProtoReflect() protoreflect.Message {
	mi := &file_ginbot_proto_utility_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

func (x *PingResp) GetMessage() string {
	if x != nil {
		if x.xxx_hidden_Message != nil {
			return *x.xxx_hidden_Message
		}
		return ""
	}
	return ""
}

func (x *PingResp) SetMessage(v string) {
	x.xxx_hidden_Message = &v
	protoimpl.X.SetPresent(&(x.XXX_presence[0]), 0, 1)
}

func (x *PingResp) HasMessage() bool {
	if x == nil {
		return false
	}
	return protoimpl.X.Present(&(x.XXX_presence[0]), 0)
}

func (x *PingResp) ClearMessage() {
	protoimpl.X.ClearPresent(&(x.XXX_presence[0]), 0)
	x.xxx_hidden_Message = nil
}

type PingResp_builder struct {
	_ [0]func() // Prevents comparability and use of unkeyed literals for the builder.

	// Pong message
	Message *string
}

func (b0 PingResp_builder) Build() *PingResp {
	m0 := &PingResp{}
	b, x := &b0, m0
	_, _ = b, x
	if b.Message != nil {
		protoimpl.X.SetPresentNonAtomic(&(x.XXX_presence[0]), 0, 1)
		x.xxx_hidden_Message = b.Message
	}
	return m0
}

var File_ginbot_proto_utility_proto protoreflect.FileDescriptor

var file_ginbot_proto_utility_proto_rawDesc = []byte{
	0x0a, 0x1a, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x75,
	0x74, 0x69, 0x6c, 0x69, 0x74, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x0c, 0x67, 0x69,
	0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1b, 0x67, 0x6f, 0x6f, 0x67,
	0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x65, 0x6d, 0x70, 0x74,
	0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x21, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x67, 0x6f, 0x5f, 0x66, 0x65, 0x61, 0x74,
	0x75, 0x72, 0x65, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x45, 0x0a, 0x0f, 0x48, 0x65,
	0x61, 0x6c, 0x74, 0x68, 0x43, 0x68, 0x65, 0x63, 0x6b, 0x52, 0x65, 0x73, 0x70, 0x12, 0x32, 0x0a,
	0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1a, 0x2e,
	0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x48, 0x65, 0x61,
	0x6c, 0x74, 0x68, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x52, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75,
	0x73, 0x22, 0x23, 0x0a, 0x07, 0x48, 0x65, 0x6c, 0x70, 0x52, 0x65, 0x71, 0x12, 0x18, 0x0a, 0x07,
	0x63, 0x6f, 0x6d, 0x6d, 0x61, 0x6e, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x63,
	0x6f, 0x6d, 0x6d, 0x61, 0x6e, 0x64, 0x22, 0x24, 0x0a, 0x08, 0x48, 0x65, 0x6c, 0x70, 0x52, 0x65,
	0x73, 0x70, 0x12, 0x18, 0x0a, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x18, 0x01, 0x20,
	0x01, 0x28, 0x09, 0x52, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x22, 0x24, 0x0a, 0x08,
	0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x73, 0x70, 0x12, 0x18, 0x0a, 0x07, 0x6d, 0x65, 0x73, 0x73,
	0x61, 0x67, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61,
	0x67, 0x65, 0x2a, 0x5c, 0x0a, 0x0c, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x53, 0x74, 0x61, 0x74,
	0x75, 0x73, 0x12, 0x1d, 0x0a, 0x19, 0x48, 0x45, 0x41, 0x4c, 0x54, 0x48, 0x5f, 0x53, 0x54, 0x41,
	0x54, 0x55, 0x53, 0x5f, 0x55, 0x4e, 0x53, 0x50, 0x45, 0x43, 0x49, 0x46, 0x49, 0x45, 0x44, 0x10,
	0x00, 0x12, 0x14, 0x0a, 0x10, 0x48, 0x45, 0x41, 0x4c, 0x54, 0x48, 0x5f, 0x53, 0x54, 0x41, 0x54,
	0x55, 0x53, 0x5f, 0x4f, 0x4b, 0x10, 0x01, 0x12, 0x17, 0x0a, 0x13, 0x48, 0x45, 0x41, 0x4c, 0x54,
	0x48, 0x5f, 0x53, 0x54, 0x41, 0x54, 0x55, 0x53, 0x5f, 0x45, 0x52, 0x52, 0x4f, 0x52, 0x10, 0x02,
	0x32, 0xc5, 0x01, 0x0a, 0x0e, 0x55, 0x74, 0x69, 0x6c, 0x69, 0x74, 0x79, 0x53, 0x65, 0x72, 0x76,
	0x69, 0x63, 0x65, 0x12, 0x44, 0x0a, 0x0b, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x43, 0x68, 0x65,
	0x63, 0x6b, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x1d, 0x2e, 0x67, 0x69, 0x6e,
	0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68,
	0x43, 0x68, 0x65, 0x63, 0x6b, 0x52, 0x65, 0x73, 0x70, 0x12, 0x35, 0x0a, 0x04, 0x48, 0x65, 0x6c,
	0x70, 0x12, 0x15, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x2e, 0x48, 0x65, 0x6c, 0x70, 0x52, 0x65, 0x71, 0x1a, 0x16, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f,
	0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e, 0x48, 0x65, 0x6c, 0x70, 0x52, 0x65, 0x73, 0x70,
	0x12, 0x36, 0x0a, 0x04, 0x50, 0x69, 0x6e, 0x67, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c,
	0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79,
	0x1a, 0x16, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2e,
	0x50, 0x69, 0x6e, 0x67, 0x52, 0x65, 0x73, 0x70, 0x42, 0x38, 0x5a, 0x2e, 0x67, 0x69, 0x74, 0x68,
	0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x6c, 0x61, 0x73, 0x69, 0x6b, 0x75, 0x75, 0x2f, 0x47,
	0x69, 0x6e, 0x42, 0x6f, 0x74, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x67, 0x65, 0x6e, 0x2f, 0x67, 0x69,
	0x6e, 0x62, 0x6f, 0x74, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x92, 0x03, 0x05, 0xd2, 0x3e, 0x02,
	0x10, 0x03, 0x62, 0x08, 0x65, 0x64, 0x69, 0x74, 0x69, 0x6f, 0x6e, 0x73, 0x70, 0xe8, 0x07,
}

var file_ginbot_proto_utility_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_ginbot_proto_utility_proto_msgTypes = make([]protoimpl.MessageInfo, 4)
var file_ginbot_proto_utility_proto_goTypes = []any{
	(HealthStatus)(0),       // 0: ginbot.proto.HealthStatus
	(*HealthCheckResp)(nil), // 1: ginbot.proto.HealthCheckResp
	(*HelpReq)(nil),         // 2: ginbot.proto.HelpReq
	(*HelpResp)(nil),        // 3: ginbot.proto.HelpResp
	(*PingResp)(nil),        // 4: ginbot.proto.PingResp
	(*emptypb.Empty)(nil),   // 5: google.protobuf.Empty
}
var file_ginbot_proto_utility_proto_depIdxs = []int32{
	0, // 0: ginbot.proto.HealthCheckResp.status:type_name -> ginbot.proto.HealthStatus
	5, // 1: ginbot.proto.UtilityService.HealthCheck:input_type -> google.protobuf.Empty
	2, // 2: ginbot.proto.UtilityService.Help:input_type -> ginbot.proto.HelpReq
	5, // 3: ginbot.proto.UtilityService.Ping:input_type -> google.protobuf.Empty
	1, // 4: ginbot.proto.UtilityService.HealthCheck:output_type -> ginbot.proto.HealthCheckResp
	3, // 5: ginbot.proto.UtilityService.Help:output_type -> ginbot.proto.HelpResp
	4, // 6: ginbot.proto.UtilityService.Ping:output_type -> ginbot.proto.PingResp
	4, // [4:7] is the sub-list for method output_type
	1, // [1:4] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_ginbot_proto_utility_proto_init() }
func file_ginbot_proto_utility_proto_init() {
	if File_ginbot_proto_utility_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_ginbot_proto_utility_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   4,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_ginbot_proto_utility_proto_goTypes,
		DependencyIndexes: file_ginbot_proto_utility_proto_depIdxs,
		EnumInfos:         file_ginbot_proto_utility_proto_enumTypes,
		MessageInfos:      file_ginbot_proto_utility_proto_msgTypes,
	}.Build()
	File_ginbot_proto_utility_proto = out.File
	file_ginbot_proto_utility_proto_rawDesc = nil
	file_ginbot_proto_utility_proto_goTypes = nil
	file_ginbot_proto_utility_proto_depIdxs = nil
}
