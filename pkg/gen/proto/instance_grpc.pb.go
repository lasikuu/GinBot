// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v5.29.2
// source: proto/instance.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	InstanceService_GetInstance_FullMethodName    = "/ginbot.instance.InstanceService/GetInstance"
	InstanceService_ListInstances_FullMethodName  = "/ginbot.instance.InstanceService/ListInstances"
	InstanceService_CreateInstance_FullMethodName = "/ginbot.instance.InstanceService/CreateInstance"
	InstanceService_UpdateInstance_FullMethodName = "/ginbot.instance.InstanceService/UpdateInstance"
	InstanceService_DeleteInstance_FullMethodName = "/ginbot.instance.InstanceService/DeleteInstance"
)

// InstanceServiceClient is the client API for InstanceService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type InstanceServiceClient interface {
	GetInstance(ctx context.Context, in *GetInstanceReq, opts ...grpc.CallOption) (*GetInstanceResp, error)
	ListInstances(ctx context.Context, in *ListInstancesReq, opts ...grpc.CallOption) (*ListInstancesResp, error)
	CreateInstance(ctx context.Context, in *CreateInstanceReq, opts ...grpc.CallOption) (*CreateInstanceResp, error)
	UpdateInstance(ctx context.Context, in *UpdateInstanceReq, opts ...grpc.CallOption) (*emptypb.Empty, error)
	DeleteInstance(ctx context.Context, in *DeleteInstanceReq, opts ...grpc.CallOption) (*emptypb.Empty, error)
}

type instanceServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewInstanceServiceClient(cc grpc.ClientConnInterface) InstanceServiceClient {
	return &instanceServiceClient{cc}
}

func (c *instanceServiceClient) GetInstance(ctx context.Context, in *GetInstanceReq, opts ...grpc.CallOption) (*GetInstanceResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetInstanceResp)
	err := c.cc.Invoke(ctx, InstanceService_GetInstance_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *instanceServiceClient) ListInstances(ctx context.Context, in *ListInstancesReq, opts ...grpc.CallOption) (*ListInstancesResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ListInstancesResp)
	err := c.cc.Invoke(ctx, InstanceService_ListInstances_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *instanceServiceClient) CreateInstance(ctx context.Context, in *CreateInstanceReq, opts ...grpc.CallOption) (*CreateInstanceResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(CreateInstanceResp)
	err := c.cc.Invoke(ctx, InstanceService_CreateInstance_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *instanceServiceClient) UpdateInstance(ctx context.Context, in *UpdateInstanceReq, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, InstanceService_UpdateInstance_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *instanceServiceClient) DeleteInstance(ctx context.Context, in *DeleteInstanceReq, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(emptypb.Empty)
	err := c.cc.Invoke(ctx, InstanceService_DeleteInstance_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InstanceServiceServer is the server API for InstanceService service.
// All implementations must embed UnimplementedInstanceServiceServer
// for forward compatibility.
type InstanceServiceServer interface {
	GetInstance(context.Context, *GetInstanceReq) (*GetInstanceResp, error)
	ListInstances(context.Context, *ListInstancesReq) (*ListInstancesResp, error)
	CreateInstance(context.Context, *CreateInstanceReq) (*CreateInstanceResp, error)
	UpdateInstance(context.Context, *UpdateInstanceReq) (*emptypb.Empty, error)
	DeleteInstance(context.Context, *DeleteInstanceReq) (*emptypb.Empty, error)
	mustEmbedUnimplementedInstanceServiceServer()
}

// UnimplementedInstanceServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedInstanceServiceServer struct{}

func (UnimplementedInstanceServiceServer) GetInstance(context.Context, *GetInstanceReq) (*GetInstanceResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetInstance not implemented")
}
func (UnimplementedInstanceServiceServer) ListInstances(context.Context, *ListInstancesReq) (*ListInstancesResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListInstances not implemented")
}
func (UnimplementedInstanceServiceServer) CreateInstance(context.Context, *CreateInstanceReq) (*CreateInstanceResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateInstance not implemented")
}
func (UnimplementedInstanceServiceServer) UpdateInstance(context.Context, *UpdateInstanceReq) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateInstance not implemented")
}
func (UnimplementedInstanceServiceServer) DeleteInstance(context.Context, *DeleteInstanceReq) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DeleteInstance not implemented")
}
func (UnimplementedInstanceServiceServer) mustEmbedUnimplementedInstanceServiceServer() {}
func (UnimplementedInstanceServiceServer) testEmbeddedByValue()                         {}

// UnsafeInstanceServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to InstanceServiceServer will
// result in compilation errors.
type UnsafeInstanceServiceServer interface {
	mustEmbedUnimplementedInstanceServiceServer()
}

func RegisterInstanceServiceServer(s grpc.ServiceRegistrar, srv InstanceServiceServer) {
	// If the following call pancis, it indicates UnimplementedInstanceServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&InstanceService_ServiceDesc, srv)
}

func _InstanceService_GetInstance_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetInstanceReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(InstanceServiceServer).GetInstance(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: InstanceService_GetInstance_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(InstanceServiceServer).GetInstance(ctx, req.(*GetInstanceReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _InstanceService_ListInstances_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ListInstancesReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(InstanceServiceServer).ListInstances(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: InstanceService_ListInstances_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(InstanceServiceServer).ListInstances(ctx, req.(*ListInstancesReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _InstanceService_CreateInstance_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateInstanceReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(InstanceServiceServer).CreateInstance(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: InstanceService_CreateInstance_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(InstanceServiceServer).CreateInstance(ctx, req.(*CreateInstanceReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _InstanceService_UpdateInstance_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdateInstanceReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(InstanceServiceServer).UpdateInstance(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: InstanceService_UpdateInstance_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(InstanceServiceServer).UpdateInstance(ctx, req.(*UpdateInstanceReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _InstanceService_DeleteInstance_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(DeleteInstanceReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(InstanceServiceServer).DeleteInstance(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: InstanceService_DeleteInstance_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(InstanceServiceServer).DeleteInstance(ctx, req.(*DeleteInstanceReq))
	}
	return interceptor(ctx, in, info, handler)
}

// InstanceService_ServiceDesc is the grpc.ServiceDesc for InstanceService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var InstanceService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ginbot.instance.InstanceService",
	HandlerType: (*InstanceServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetInstance",
			Handler:    _InstanceService_GetInstance_Handler,
		},
		{
			MethodName: "ListInstances",
			Handler:    _InstanceService_ListInstances_Handler,
		},
		{
			MethodName: "CreateInstance",
			Handler:    _InstanceService_CreateInstance_Handler,
		},
		{
			MethodName: "UpdateInstance",
			Handler:    _InstanceService_UpdateInstance_Handler,
		},
		{
			MethodName: "DeleteInstance",
			Handler:    _InstanceService_DeleteInstance_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "proto/instance.proto",
}
