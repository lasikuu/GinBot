// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.1
// 	protoc        v5.29.2
// source: proto/entertainment.proto

package proto

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
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

type GetDoublesPlusNResp struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Number padded with 0s
	Number        *int32 `protobuf:"varint,1,opt,name=number" json:"number,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetDoublesPlusNResp) Reset() {
	*x = GetDoublesPlusNResp{}
	mi := &file_proto_entertainment_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetDoublesPlusNResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetDoublesPlusNResp) ProtoMessage() {}

func (x *GetDoublesPlusNResp) ProtoReflect() protoreflect.Message {
	mi := &file_proto_entertainment_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetDoublesPlusNResp.ProtoReflect.Descriptor instead.
func (*GetDoublesPlusNResp) Descriptor() ([]byte, []int) {
	return file_proto_entertainment_proto_rawDescGZIP(), []int{0}
}

func (x *GetDoublesPlusNResp) GetNumber() int32 {
	if x != nil && x.Number != nil {
		return *x.Number
	}
	return 0
}

type SetBirthdayReq struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// User ID to set the birthday for
	UserId *int64 `protobuf:"varint,1,opt,name=user_id,json=userId" json:"user_id,omitempty"`
	// Birthday to set
	Birthday      *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=birthday" json:"birthday,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SetBirthdayReq) Reset() {
	*x = SetBirthdayReq{}
	mi := &file_proto_entertainment_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SetBirthdayReq) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SetBirthdayReq) ProtoMessage() {}

func (x *SetBirthdayReq) ProtoReflect() protoreflect.Message {
	mi := &file_proto_entertainment_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SetBirthdayReq.ProtoReflect.Descriptor instead.
func (*SetBirthdayReq) Descriptor() ([]byte, []int) {
	return file_proto_entertainment_proto_rawDescGZIP(), []int{1}
}

func (x *SetBirthdayReq) GetUserId() int64 {
	if x != nil && x.UserId != nil {
		return *x.UserId
	}
	return 0
}

func (x *SetBirthdayReq) GetBirthday() *timestamppb.Timestamp {
	if x != nil {
		return x.Birthday
	}
	return nil
}

type SetBirthdayResp struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// User ID of the user with the set birthday
	UserId *int64 `protobuf:"varint,1,opt,name=user_id,json=userId" json:"user_id,omitempty"`
	// Birthday of the user
	Birthday      *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=birthday" json:"birthday,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SetBirthdayResp) Reset() {
	*x = SetBirthdayResp{}
	mi := &file_proto_entertainment_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SetBirthdayResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SetBirthdayResp) ProtoMessage() {}

func (x *SetBirthdayResp) ProtoReflect() protoreflect.Message {
	mi := &file_proto_entertainment_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SetBirthdayResp.ProtoReflect.Descriptor instead.
func (*SetBirthdayResp) Descriptor() ([]byte, []int) {
	return file_proto_entertainment_proto_rawDescGZIP(), []int{2}
}

func (x *SetBirthdayResp) GetUserId() int64 {
	if x != nil && x.UserId != nil {
		return *x.UserId
	}
	return 0
}

func (x *SetBirthdayResp) GetBirthday() *timestamppb.Timestamp {
	if x != nil {
		return x.Birthday
	}
	return nil
}

var File_proto_entertainment_proto protoreflect.FileDescriptor

var file_proto_entertainment_proto_rawDesc = []byte{
	0x0a, 0x19, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69,
	0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x14, 0x67, 0x69, 0x6e,
	0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e,
	0x74, 0x1a, 0x1f, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62,
	0x75, 0x66, 0x2f, 0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x1a, 0x1b, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2f, 0x65, 0x6d, 0x70, 0x74, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22,
	0x2d, 0x0a, 0x13, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75,
	0x73, 0x4e, 0x52, 0x65, 0x73, 0x70, 0x12, 0x16, 0x0a, 0x06, 0x6e, 0x75, 0x6d, 0x62, 0x65, 0x72,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x05, 0x52, 0x06, 0x6e, 0x75, 0x6d, 0x62, 0x65, 0x72, 0x22, 0x61,
	0x0a, 0x0e, 0x53, 0x65, 0x74, 0x42, 0x69, 0x72, 0x74, 0x68, 0x64, 0x61, 0x79, 0x52, 0x65, 0x71,
	0x12, 0x17, 0x0a, 0x07, 0x75, 0x73, 0x65, 0x72, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x03, 0x52, 0x06, 0x75, 0x73, 0x65, 0x72, 0x49, 0x64, 0x12, 0x36, 0x0a, 0x08, 0x62, 0x69, 0x72,
	0x74, 0x68, 0x64, 0x61, 0x79, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1a, 0x2e, 0x67, 0x6f,
	0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x54, 0x69,
	0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52, 0x08, 0x62, 0x69, 0x72, 0x74, 0x68, 0x64, 0x61,
	0x79, 0x22, 0x62, 0x0a, 0x0f, 0x53, 0x65, 0x74, 0x42, 0x69, 0x72, 0x74, 0x68, 0x64, 0x61, 0x79,
	0x52, 0x65, 0x73, 0x70, 0x12, 0x17, 0x0a, 0x07, 0x75, 0x73, 0x65, 0x72, 0x5f, 0x69, 0x64, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x03, 0x52, 0x06, 0x75, 0x73, 0x65, 0x72, 0x49, 0x64, 0x12, 0x36, 0x0a,
	0x08, 0x62, 0x69, 0x72, 0x74, 0x68, 0x64, 0x61, 0x79, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32,
	0x1a, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75,
	0x66, 0x2e, 0x54, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52, 0x08, 0x62, 0x69, 0x72,
	0x74, 0x68, 0x64, 0x61, 0x79, 0x32, 0xd8, 0x06, 0x0a, 0x14, 0x45, 0x6e, 0x74, 0x65, 0x72, 0x74,
	0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x12, 0x4f,
	0x0a, 0x0a, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e, 0x67,
	0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45,
	0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e,
	0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74, 0x44,
	0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70, 0x12,
	0x4f, 0x0a, 0x0a, 0x47, 0x65, 0x74, 0x54, 0x72, 0x69, 0x70, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e,
	0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e,
	0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65,
	0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74,
	0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70,
	0x12, 0x52, 0x0a, 0x0d, 0x47, 0x65, 0x74, 0x51, 0x75, 0x61, 0x64, 0x72, 0x75, 0x70, 0x6c, 0x65,
	0x73, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62,
	0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74,
	0x2e, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e,
	0x52, 0x65, 0x73, 0x70, 0x12, 0x52, 0x0a, 0x0d, 0x47, 0x65, 0x74, 0x51, 0x75, 0x69, 0x6e, 0x74,
	0x75, 0x70, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e,
	0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e,
	0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50,
	0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70, 0x12, 0x51, 0x0a, 0x0c, 0x47, 0x65, 0x74, 0x53,
	0x65, 0x78, 0x74, 0x75, 0x70, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c,
	0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79,
	0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74,
	0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c,
	0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70, 0x12, 0x51, 0x0a, 0x0c, 0x47,
	0x65, 0x74, 0x53, 0x65, 0x70, 0x74, 0x75, 0x70, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e, 0x67, 0x6f,
	0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d,
	0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74,
	0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74, 0x44, 0x6f,
	0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70, 0x12, 0x50,
	0x0a, 0x0b, 0x47, 0x65, 0x74, 0x4f, 0x63, 0x74, 0x75, 0x70, 0x6c, 0x65, 0x73, 0x12, 0x16, 0x2e,
	0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e,
	0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65,
	0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47, 0x65, 0x74,
	0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65, 0x73, 0x70,
	0x12, 0x50, 0x0a, 0x0b, 0x47, 0x65, 0x74, 0x4e, 0x6f, 0x6e, 0x75, 0x70, 0x6c, 0x65, 0x73, 0x12,
	0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75,
	0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74,
	0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x47,
	0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e, 0x52, 0x65,
	0x73, 0x70, 0x12, 0x50, 0x0a, 0x0b, 0x47, 0x65, 0x74, 0x44, 0x65, 0x63, 0x75, 0x70, 0x6c, 0x65,
	0x73, 0x12, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x1a, 0x29, 0x2e, 0x67, 0x69, 0x6e, 0x62,
	0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74,
	0x2e, 0x47, 0x65, 0x74, 0x44, 0x6f, 0x75, 0x62, 0x6c, 0x65, 0x73, 0x50, 0x6c, 0x75, 0x73, 0x4e,
	0x52, 0x65, 0x73, 0x70, 0x12, 0x5a, 0x0a, 0x0b, 0x53, 0x65, 0x74, 0x42, 0x69, 0x72, 0x74, 0x68,
	0x64, 0x61, 0x79, 0x12, 0x24, 0x2e, 0x67, 0x69, 0x6e, 0x62, 0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74,
	0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x2e, 0x53, 0x65, 0x74, 0x42, 0x69,
	0x72, 0x74, 0x68, 0x64, 0x61, 0x79, 0x52, 0x65, 0x71, 0x1a, 0x25, 0x2e, 0x67, 0x69, 0x6e, 0x62,
	0x6f, 0x74, 0x2e, 0x65, 0x6e, 0x74, 0x65, 0x72, 0x74, 0x61, 0x69, 0x6e, 0x6d, 0x65, 0x6e, 0x74,
	0x2e, 0x53, 0x65, 0x74, 0x42, 0x69, 0x72, 0x74, 0x68, 0x64, 0x61, 0x79, 0x52, 0x65, 0x73, 0x70,
	0x42, 0x30, 0x5a, 0x2e, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x43,
	0x72, 0x65, 0x73, 0x63, 0x65, 0x6e, 0x74, 0x4b, 0x6f, 0x68, 0x61, 0x6e, 0x61, 0x2f, 0x47, 0x69,
	0x6e, 0x42, 0x6f, 0x74, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x67, 0x65, 0x6e, 0x2f, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x62, 0x08, 0x65, 0x64, 0x69, 0x74, 0x69, 0x6f, 0x6e, 0x73, 0x70, 0xe8, 0x07,
}

var (
	file_proto_entertainment_proto_rawDescOnce sync.Once
	file_proto_entertainment_proto_rawDescData = file_proto_entertainment_proto_rawDesc
)

func file_proto_entertainment_proto_rawDescGZIP() []byte {
	file_proto_entertainment_proto_rawDescOnce.Do(func() {
		file_proto_entertainment_proto_rawDescData = protoimpl.X.CompressGZIP(file_proto_entertainment_proto_rawDescData)
	})
	return file_proto_entertainment_proto_rawDescData
}

var file_proto_entertainment_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_proto_entertainment_proto_goTypes = []any{
	(*GetDoublesPlusNResp)(nil),   // 0: ginbot.entertainment.GetDoublesPlusNResp
	(*SetBirthdayReq)(nil),        // 1: ginbot.entertainment.SetBirthdayReq
	(*SetBirthdayResp)(nil),       // 2: ginbot.entertainment.SetBirthdayResp
	(*timestamppb.Timestamp)(nil), // 3: google.protobuf.Timestamp
	(*emptypb.Empty)(nil),         // 4: google.protobuf.Empty
}
var file_proto_entertainment_proto_depIdxs = []int32{
	3,  // 0: ginbot.entertainment.SetBirthdayReq.birthday:type_name -> google.protobuf.Timestamp
	3,  // 1: ginbot.entertainment.SetBirthdayResp.birthday:type_name -> google.protobuf.Timestamp
	4,  // 2: ginbot.entertainment.EntertainmentService.GetDoubles:input_type -> google.protobuf.Empty
	4,  // 3: ginbot.entertainment.EntertainmentService.GetTriples:input_type -> google.protobuf.Empty
	4,  // 4: ginbot.entertainment.EntertainmentService.GetQuadruples:input_type -> google.protobuf.Empty
	4,  // 5: ginbot.entertainment.EntertainmentService.GetQuintuples:input_type -> google.protobuf.Empty
	4,  // 6: ginbot.entertainment.EntertainmentService.GetSextuples:input_type -> google.protobuf.Empty
	4,  // 7: ginbot.entertainment.EntertainmentService.GetSeptuples:input_type -> google.protobuf.Empty
	4,  // 8: ginbot.entertainment.EntertainmentService.GetOctuples:input_type -> google.protobuf.Empty
	4,  // 9: ginbot.entertainment.EntertainmentService.GetNonuples:input_type -> google.protobuf.Empty
	4,  // 10: ginbot.entertainment.EntertainmentService.GetDecuples:input_type -> google.protobuf.Empty
	1,  // 11: ginbot.entertainment.EntertainmentService.SetBirthday:input_type -> ginbot.entertainment.SetBirthdayReq
	0,  // 12: ginbot.entertainment.EntertainmentService.GetDoubles:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 13: ginbot.entertainment.EntertainmentService.GetTriples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 14: ginbot.entertainment.EntertainmentService.GetQuadruples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 15: ginbot.entertainment.EntertainmentService.GetQuintuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 16: ginbot.entertainment.EntertainmentService.GetSextuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 17: ginbot.entertainment.EntertainmentService.GetSeptuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 18: ginbot.entertainment.EntertainmentService.GetOctuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 19: ginbot.entertainment.EntertainmentService.GetNonuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	0,  // 20: ginbot.entertainment.EntertainmentService.GetDecuples:output_type -> ginbot.entertainment.GetDoublesPlusNResp
	2,  // 21: ginbot.entertainment.EntertainmentService.SetBirthday:output_type -> ginbot.entertainment.SetBirthdayResp
	12, // [12:22] is the sub-list for method output_type
	2,  // [2:12] is the sub-list for method input_type
	2,  // [2:2] is the sub-list for extension type_name
	2,  // [2:2] is the sub-list for extension extendee
	0,  // [0:2] is the sub-list for field type_name
}

func init() { file_proto_entertainment_proto_init() }
func file_proto_entertainment_proto_init() {
	if File_proto_entertainment_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proto_entertainment_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proto_entertainment_proto_goTypes,
		DependencyIndexes: file_proto_entertainment_proto_depIdxs,
		MessageInfos:      file_proto_entertainment_proto_msgTypes,
	}.Build()
	File_proto_entertainment_proto = out.File
	file_proto_entertainment_proto_rawDesc = nil
	file_proto_entertainment_proto_goTypes = nil
	file_proto_entertainment_proto_depIdxs = nil
}
