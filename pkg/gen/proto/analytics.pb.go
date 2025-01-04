// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.1
// 	protoc        v5.29.2
// source: proto/analytics.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ActionType int32

const (
	ActionType_UNKNOWN            ActionType = 0
	ActionType_TRIGGER_OCCURRED   ActionType = 1
	ActionType_TRIGGER_CALLED     ActionType = 2
	ActionType_REMINDER_CREATED   ActionType = 3
	ActionType_REMINDER_DELIVERED ActionType = 4
	ActionType_SQUAD_RALLIED      ActionType = 5
	ActionType_SQUAD_FORMED       ActionType = 6
)

// Enum value maps for ActionType.
var (
	ActionType_name = map[int32]string{
		0: "UNKNOWN",
		1: "TRIGGER_OCCURRED",
		2: "TRIGGER_CALLED",
		3: "REMINDER_CREATED",
		4: "REMINDER_DELIVERED",
		5: "SQUAD_RALLIED",
		6: "SQUAD_FORMED",
	}
	ActionType_value = map[string]int32{
		"UNKNOWN":            0,
		"TRIGGER_OCCURRED":   1,
		"TRIGGER_CALLED":     2,
		"REMINDER_CREATED":   3,
		"REMINDER_DELIVERED": 4,
		"SQUAD_RALLIED":      5,
		"SQUAD_FORMED":       6,
	}
)

func (x ActionType) Enum() *ActionType {
	p := new(ActionType)
	*p = x
	return p
}

func (x ActionType) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ActionType) Descriptor() protoreflect.EnumDescriptor {
	return file_proto_analytics_proto_enumTypes[0].Descriptor()
}

func (ActionType) Type() protoreflect.EnumType {
	return &file_proto_analytics_proto_enumTypes[0]
}

func (x ActionType) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ActionType.Descriptor instead.
func (ActionType) EnumDescriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{0}
}

// Message representing a ActionRecord
type ActionRecord struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID of the Action
	Id *int64 `protobuf:"varint,1,opt,name=id" json:"id,omitempty"`
	// Type of the Action
	ActionType *ActionType `protobuf:"varint,2,opt,name=action_type,json=actionType,enum=ginbot.analytics.ActionType" json:"action_type,omitempty"`
	// Date and time of the Action
	ActionTimestamp *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=action_timestamp,json=actionTimestamp" json:"action_timestamp,omitempty"`
	// Time taken to perform the Action in milliseconds
	ActionTime *int64 `protobuf:"varint,4,opt,name=action_time,json=actionTime" json:"action_time,omitempty"`
	// User ID of the actor
	ActorId       *int64 `protobuf:"varint,5,opt,name=actor_id,json=actorId" json:"actor_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ActionRecord) Reset() {
	*x = ActionRecord{}
	mi := &file_proto_analytics_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ActionRecord) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ActionRecord) ProtoMessage() {}

func (x *ActionRecord) ProtoReflect() protoreflect.Message {
	mi := &file_proto_analytics_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ActionRecord.ProtoReflect.Descriptor instead.
func (*ActionRecord) Descriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{0}
}

func (x *ActionRecord) GetId() int64 {
	if x != nil && x.Id != nil {
		return *x.Id
	}
	return 0
}

func (x *ActionRecord) GetActionType() ActionType {
	if x != nil && x.ActionType != nil {
		return *x.ActionType
	}
	return ActionType_UNKNOWN
}

func (x *ActionRecord) GetActionTimestamp() *timestamppb.Timestamp {
	if x != nil {
		return x.ActionTimestamp
	}
	return nil
}

func (x *ActionRecord) GetActionTime() int64 {
	if x != nil && x.ActionTime != nil {
		return *x.ActionTime
	}
	return 0
}

func (x *ActionRecord) GetActorId() int64 {
	if x != nil && x.ActorId != nil {
		return *x.ActorId
	}
	return 0
}

type CreateActionRecordReq struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Type of the Action
	ActionType *ActionType `protobuf:"varint,1,opt,name=action_type,json=actionType,enum=ginbot.analytics.ActionType" json:"action_type,omitempty"`
	// Time taken to perform the Action in milliseconds
	ActionTime *int64 `protobuf:"varint,2,opt,name=action_time,json=actionTime" json:"action_time,omitempty"`
	// User ID of the actor
	ActorId       *int64 `protobuf:"varint,3,opt,name=actor_id,json=actorId" json:"actor_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateActionRecordReq) Reset() {
	*x = CreateActionRecordReq{}
	mi := &file_proto_analytics_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateActionRecordReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateActionRecordReq) ProtoMessage() {}

func (x *CreateActionRecordReq) ProtoReflect() protoreflect.Message {
	mi := &file_proto_analytics_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateActionRecordReq.ProtoReflect.Descriptor instead.
func (*CreateActionRecordReq) Descriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{1}
}

func (x *CreateActionRecordReq) GetActionType() ActionType {
	if x != nil && x.ActionType != nil {
		return *x.ActionType
	}
	return ActionType_UNKNOWN
}

func (x *CreateActionRecordReq) GetActionTime() int64 {
	if x != nil && x.ActionTime != nil {
		return *x.ActionTime
	}
	return 0
}

func (x *CreateActionRecordReq) GetActorId() int64 {
	if x != nil && x.ActorId != nil {
		return *x.ActorId
	}
	return 0
}

type CreateActionRecordResp struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID of the ActionRecord
	Id            *int64 `protobuf:"varint,1,opt,name=id" json:"id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CreateActionRecordResp) Reset() {
	*x = CreateActionRecordResp{}
	mi := &file_proto_analytics_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CreateActionRecordResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CreateActionRecordResp) ProtoMessage() {}

func (x *CreateActionRecordResp) ProtoReflect() protoreflect.Message {
	mi := &file_proto_analytics_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CreateActionRecordResp.ProtoReflect.Descriptor instead.
func (*CreateActionRecordResp) Descriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{2}
}

func (x *CreateActionRecordResp) GetId() int64 {
	if x != nil && x.Id != nil {
		return *x.Id
	}
	return 0
}

type ListActionRecordsReq struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Limit the number of recorded actions returned
	Limit *int64 `protobuf:"varint,1,opt,name=limit" json:"limit,omitempty"`
	// Offset the recorded actions returned
	Offset *int64 `protobuf:"varint,2,opt,name=offset" json:"offset,omitempty"`
	// Type of the action to search for
	ActionType *ActionType `protobuf:"varint,3,opt,name=action_type,json=actionType,enum=ginbot.analytics.ActionType" json:"action_type,omitempty"`
	// Search for recorded actions by this actor
	ActorId *int64 `protobuf:"varint,4,opt,name=actor_id,json=actorId" json:"actor_id,omitempty"`
	// Search for recorded actions starting from this date
	PeriodStart *timestamppb.Timestamp `protobuf:"bytes,5,opt,name=period_start,json=periodStart" json:"period_start,omitempty"`
	// Search for recorded actions ending at this date
	PeriodEnd     *timestamppb.Timestamp `protobuf:"bytes,6,opt,name=period_end,json=periodEnd" json:"period_end,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListActionRecordsReq) Reset() {
	*x = ListActionRecordsReq{}
	mi := &file_proto_analytics_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListActionRecordsReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListActionRecordsReq) ProtoMessage() {}

func (x *ListActionRecordsReq) ProtoReflect() protoreflect.Message {
	mi := &file_proto_analytics_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListActionRecordsReq.ProtoReflect.Descriptor instead.
func (*ListActionRecordsReq) Descriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{3}
}

func (x *ListActionRecordsReq) GetLimit() int64 {
	if x != nil && x.Limit != nil {
		return *x.Limit
	}
	return 0
}

func (x *ListActionRecordsReq) GetOffset() int64 {
	if x != nil && x.Offset != nil {
		return *x.Offset
	}
	return 0
}

func (x *ListActionRecordsReq) GetActionType() ActionType {
	if x != nil && x.ActionType != nil {
		return *x.ActionType
	}
	return ActionType_UNKNOWN
}

func (x *ListActionRecordsReq) GetActorId() int64 {
	if x != nil && x.ActorId != nil {
		return *x.ActorId
	}
	return 0
}

func (x *ListActionRecordsReq) GetPeriodStart() *timestamppb.Timestamp {
	if x != nil {
		return x.PeriodStart
	}
	return nil
}

func (x *ListActionRecordsReq) GetPeriodEnd() *timestamppb.Timestamp {
	if x != nil {
		return x.PeriodEnd
	}
	return nil
}

type ListActionRecordsResp struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ActionRecords []*ActionRecord        `protobuf:"bytes,1,rep,name=action_records,json=actionRecords" json:"action_records,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListActionRecordsResp) Reset() {
	*x = ListActionRecordsResp{}
	mi := &file_proto_analytics_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListActionRecordsResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListActionRecordsResp) ProtoMessage() {}

func (x *ListActionRecordsResp) ProtoReflect() protoreflect.Message {
	mi := &file_proto_analytics_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListActionRecordsResp.ProtoReflect.Descriptor instead.
func (*ListActionRecordsResp) Descriptor() ([]byte, []int) {
	return file_proto_analytics_proto_rawDescGZIP(), []int{4}
}

func (x *ListActionRecordsResp) GetActionRecords() []*ActionRecord {
	if x != nil {
		return x.ActionRecords
	}
	return nil
}

var File_proto_analytics_proto protoreflect.FileDescriptor

var file_proto_analytics_proto_rawDesc = []byte{
	0x0a, 0x15, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63,
	0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x10, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e,
	0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x1a, 0x1f, 0x67, 0x6f, 0x6f, 0x67, 0x6c,
	0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x74, 0x69, 0x6d, 0x65, 0x73,
	0x74, 0x61, 0x6d, 0x70, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0xe0, 0x01, 0x0a, 0x0c, 0x41,
	0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x12, 0x0e, 0x0a, 0x02, 0x69,
	0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x03, 0x52, 0x02, 0x69, 0x64, 0x12, 0x3d, 0x0a, 0x0b, 0x61,
	0x63, 0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x74, 0x79, 0x70, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0e,
	0x32, 0x1c, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74,
	0x69, 0x63, 0x73, 0x2e, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x52, 0x0a,
	0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x12, 0x45, 0x0a, 0x10, 0x61, 0x63,
	0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x18, 0x03,
	0x20, 0x01, 0x28, 0x0b, 0x32, 0x1a, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70,
	0x52, 0x0f, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d,
	0x70, 0x12, 0x1f, 0x0a, 0x0b, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x74, 0x69, 0x6d, 0x65,
	0x18, 0x04, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0a, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x69,
	0x6d, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x61, 0x63, 0x74, 0x6f, 0x72, 0x5f, 0x69, 0x64, 0x18, 0x05,
	0x20, 0x01, 0x28, 0x03, 0x52, 0x07, 0x61, 0x63, 0x74, 0x6f, 0x72, 0x49, 0x64, 0x22, 0x92, 0x01,
	0x0a, 0x15, 0x43, 0x72, 0x65, 0x61, 0x74, 0x65, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65,
	0x63, 0x6f, 0x72, 0x64, 0x52, 0x65, 0x71, 0x12, 0x3d, 0x0a, 0x0b, 0x61, 0x63, 0x74, 0x69, 0x6f,
	0x6e, 0x5f, 0x74, 0x79, 0x70, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1c, 0x2e, 0x67,
	0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e,
	0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x52, 0x0a, 0x61, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x12, 0x1f, 0x0a, 0x0b, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e,
	0x5f, 0x74, 0x69, 0x6d, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0a, 0x61, 0x63, 0x74,
	0x69, 0x6f, 0x6e, 0x54, 0x69, 0x6d, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x61, 0x63, 0x74, 0x6f, 0x72,
	0x5f, 0x69, 0x64, 0x18, 0x03, 0x20, 0x01, 0x28, 0x03, 0x52, 0x07, 0x61, 0x63, 0x74, 0x6f, 0x72,
	0x49, 0x64, 0x22, 0x28, 0x0a, 0x16, 0x43, 0x72, 0x65, 0x61, 0x74, 0x65, 0x41, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x52, 0x65, 0x73, 0x70, 0x12, 0x0e, 0x0a, 0x02,
	0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x03, 0x52, 0x02, 0x69, 0x64, 0x22, 0x98, 0x02, 0x0a,
	0x14, 0x4c, 0x69, 0x73, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72,
	0x64, 0x73, 0x52, 0x65, 0x71, 0x12, 0x14, 0x0a, 0x05, 0x6c, 0x69, 0x6d, 0x69, 0x74, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x03, 0x52, 0x05, 0x6c, 0x69, 0x6d, 0x69, 0x74, 0x12, 0x16, 0x0a, 0x06, 0x6f,
	0x66, 0x66, 0x73, 0x65, 0x74, 0x18, 0x02, 0x20, 0x01, 0x28, 0x03, 0x52, 0x06, 0x6f, 0x66, 0x66,
	0x73, 0x65, 0x74, 0x12, 0x3d, 0x0a, 0x0b, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x74, 0x79,
	0x70, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1c, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f,
	0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x41, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x52, 0x0a, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x54, 0x79,
	0x70, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x61, 0x63, 0x74, 0x6f, 0x72, 0x5f, 0x69, 0x64, 0x18, 0x04,
	0x20, 0x01, 0x28, 0x03, 0x52, 0x07, 0x61, 0x63, 0x74, 0x6f, 0x72, 0x49, 0x64, 0x12, 0x3d, 0x0a,
	0x0c, 0x70, 0x65, 0x72, 0x69, 0x6f, 0x64, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x18, 0x05, 0x20,
	0x01, 0x28, 0x0b, 0x32, 0x1a, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52,
	0x0b, 0x70, 0x65, 0x72, 0x69, 0x6f, 0x64, 0x53, 0x74, 0x61, 0x72, 0x74, 0x12, 0x39, 0x0a, 0x0a,
	0x70, 0x65, 0x72, 0x69, 0x6f, 0x64, 0x5f, 0x65, 0x6e, 0x64, 0x18, 0x06, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x1a, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62,
	0x75, 0x66, 0x2e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52, 0x09, 0x70, 0x65,
	0x72, 0x69, 0x6f, 0x64, 0x45, 0x6e, 0x64, 0x22, 0x5e, 0x0a, 0x15, 0x4c, 0x69, 0x73, 0x74, 0x41,
	0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x73, 0x52, 0x65, 0x73, 0x70,
	0x12, 0x45, 0x0a, 0x0e, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x72, 0x65, 0x63, 0x6f, 0x72,
	0x64, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x1e, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f,
	0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x41, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x52, 0x0d, 0x61, 0x63, 0x74, 0x69, 0x6f, 0x6e,
	0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x73, 0x2a, 0x96, 0x01, 0x0a, 0x0a, 0x41, 0x63, 0x74, 0x69,
	0x6f, 0x6e, 0x54, 0x79, 0x70, 0x65, 0x12, 0x0b, 0x0a, 0x07, 0x55, 0x4e, 0x4b, 0x4e, 0x4f, 0x57,
	0x4e, 0x10, 0x00, 0x12, 0x14, 0x0a, 0x10, 0x54, 0x52, 0x49, 0x47, 0x47, 0x45, 0x52, 0x5f, 0x4f,
	0x43, 0x43, 0x55, 0x52, 0x52, 0x45, 0x44, 0x10, 0x01, 0x12, 0x12, 0x0a, 0x0e, 0x54, 0x52, 0x49,
	0x47, 0x47, 0x45, 0x52, 0x5f, 0x43, 0x41, 0x4c, 0x4c, 0x45, 0x44, 0x10, 0x02, 0x12, 0x14, 0x0a,
	0x10, 0x52, 0x45, 0x4d, 0x49, 0x4e, 0x44, 0x45, 0x52, 0x5f, 0x43, 0x52, 0x45, 0x41, 0x54, 0x45,
	0x44, 0x10, 0x03, 0x12, 0x16, 0x0a, 0x12, 0x52, 0x45, 0x4d, 0x49, 0x4e, 0x44, 0x45, 0x52, 0x5f,
	0x44, 0x45, 0x4c, 0x49, 0x56, 0x45, 0x52, 0x45, 0x44, 0x10, 0x04, 0x12, 0x11, 0x0a, 0x0d, 0x53,
	0x51, 0x55, 0x41, 0x44, 0x5f, 0x52, 0x41, 0x4c, 0x4c, 0x49, 0x45, 0x44, 0x10, 0x05, 0x12, 0x10,
	0x0a, 0x0c, 0x53, 0x51, 0x55, 0x41, 0x44, 0x5f, 0x46, 0x4f, 0x52, 0x4d, 0x45, 0x44, 0x10, 0x06,
	0x32, 0xe1, 0x01, 0x0a, 0x10, 0x41, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x53, 0x65,
	0x72, 0x76, 0x69, 0x63, 0x65, 0x12, 0x67, 0x0a, 0x12, 0x43, 0x72, 0x65, 0x61, 0x74, 0x65, 0x41,
	0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x12, 0x27, 0x2e, 0x67, 0x69,
	0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x43,
	0x72, 0x65, 0x61, 0x74, 0x65, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72,
	0x64, 0x52, 0x65, 0x71, 0x1a, 0x28, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e,
	0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x43, 0x72, 0x65, 0x61, 0x74, 0x65, 0x41, 0x63,
	0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x52, 0x65, 0x73, 0x70, 0x12, 0x64,
	0x0a, 0x11, 0x4c, 0x69, 0x73, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f,
	0x72, 0x64, 0x73, 0x12, 0x26, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e, 0x61,
	0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x4c, 0x69, 0x73, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f,
	0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x73, 0x52, 0x65, 0x71, 0x1a, 0x27, 0x2e, 0x67, 0x69,
	0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x61, 0x6e, 0x61, 0x6c, 0x79, 0x74, 0x69, 0x63, 0x73, 0x2e, 0x4c,
	0x69, 0x73, 0x74, 0x41, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x63, 0x6f, 0x72, 0x64, 0x73,
	0x52, 0x65, 0x73, 0x70, 0x42, 0x30, 0x5a, 0x2e, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63,
	0x6f, 0x6d, 0x2f, 0x43, 0x72, 0x65, 0x73, 0x63, 0x65, 0x6e, 0x74, 0x4b, 0x6f, 0x68, 0x61, 0x6e,
	0x61, 0x2f, 0x47, 0x69, 0x6e, 0x42, 0x6f, 0x74, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x67, 0x65, 0x6e,
	0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x08, 0x65, 0x64, 0x69, 0x74, 0x69, 0x6f, 0x6e, 0x73,
	0x70, 0xe8, 0x07,
}

var (
	file_proto_analytics_proto_rawDescOnce sync.Once
	file_proto_analytics_proto_rawDescData = file_proto_analytics_proto_rawDesc
)

func file_proto_analytics_proto_rawDescGZIP() []byte {
	file_proto_analytics_proto_rawDescOnce.Do(func() {
		file_proto_analytics_proto_rawDescData = protoimpl.X.CompressGZIP(file_proto_analytics_proto_rawDescData)
	})
	return file_proto_analytics_proto_rawDescData
}

var file_proto_analytics_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_proto_analytics_proto_msgTypes = make([]protoimpl.MessageInfo, 5)
var file_proto_analytics_proto_goTypes = []any{
	(ActionType)(0),                // 0: ginbot.analytics.ActionType
	(*ActionRecord)(nil),           // 1: ginbot.analytics.ActionRecord
	(*CreateActionRecordReq)(nil),  // 2: ginbot.analytics.CreateActionRecordReq
	(*CreateActionRecordResp)(nil), // 3: ginbot.analytics.CreateActionRecordResp
	(*ListActionRecordsReq)(nil),   // 4: ginbot.analytics.ListActionRecordsReq
	(*ListActionRecordsResp)(nil),  // 5: ginbot.analytics.ListActionRecordsResp
	(*timestamppb.Timestamp)(nil),  // 6: google.protobuf.Timestamp
}
var file_proto_analytics_proto_depIdxs = []int32{
	0, // 0: ginbot.analytics.ActionRecord.action_type:type_name -> ginbot.analytics.ActionType
	6, // 1: ginbot.analytics.ActionRecord.action_timestamp:type_name -> google.protobuf.Timestamp
	0, // 2: ginbot.analytics.CreateActionRecordReq.action_type:type_name -> ginbot.analytics.ActionType
	0, // 3: ginbot.analytics.ListActionRecordsReq.action_type:type_name -> ginbot.analytics.ActionType
	6, // 4: ginbot.analytics.ListActionRecordsReq.period_start:type_name -> google.protobuf.Timestamp
	6, // 5: ginbot.analytics.ListActionRecordsReq.period_end:type_name -> google.protobuf.Timestamp
	1, // 6: ginbot.analytics.ListActionRecordsResp.action_records:type_name -> ginbot.analytics.ActionRecord
	2, // 7: ginbot.analytics.AnalyticsService.CreateActionRecord:input_type -> ginbot.analytics.CreateActionRecordReq
	4, // 8: ginbot.analytics.AnalyticsService.ListActionRecords:input_type -> ginbot.analytics.ListActionRecordsReq
	3, // 9: ginbot.analytics.AnalyticsService.CreateActionRecord:output_type -> ginbot.analytics.CreateActionRecordResp
	5, // 10: ginbot.analytics.AnalyticsService.ListActionRecords:output_type -> ginbot.analytics.ListActionRecordsResp
	9, // [9:11] is the sub-list for method output_type
	7, // [7:9] is the sub-list for method input_type
	7, // [7:7] is the sub-list for extension type_name
	7, // [7:7] is the sub-list for extension extendee
	0, // [0:7] is the sub-list for field type_name
}

func init() { file_proto_analytics_proto_init() }
func file_proto_analytics_proto_init() {
	if File_proto_analytics_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proto_analytics_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   5,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proto_analytics_proto_goTypes,
		DependencyIndexes: file_proto_analytics_proto_depIdxs,
		EnumInfos:         file_proto_analytics_proto_enumTypes,
		MessageInfos:      file_proto_analytics_proto_msgTypes,
	}.Build()
	File_proto_analytics_proto = out.File
	file_proto_analytics_proto_rawDesc = nil
	file_proto_analytics_proto_goTypes = nil
	file_proto_analytics_proto_depIdxs = nil
}
