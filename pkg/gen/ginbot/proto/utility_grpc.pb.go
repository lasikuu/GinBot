// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v5.29.2
// source: ginbot/proto/utility.proto

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
	UtilityService_HealthCheck_FullMethodName = "/ginbot.proto.UtilityService/HealthCheck"
	UtilityService_Help_FullMethodName        = "/ginbot.proto.UtilityService/Help"
	UtilityService_Ping_FullMethodName        = "/ginbot.proto.UtilityService/Ping"
)

// UtilityServiceClient is the client API for UtilityService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type UtilityServiceClient interface {
	HealthCheck(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*HealthCheckResp, error)
	Help(ctx context.Context, in *HelpReq, opts ...grpc.CallOption) (*HelpResp, error)
	Ping(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*PingResp, error)
}

type utilityServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewUtilityServiceClient(cc grpc.ClientConnInterface) UtilityServiceClient {
	return &utilityServiceClient{cc}
}

func (c *utilityServiceClient) HealthCheck(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*HealthCheckResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(HealthCheckResp)
	err := c.cc.Invoke(ctx, UtilityService_HealthCheck_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *utilityServiceClient) Help(ctx context.Context, in *HelpReq, opts ...grpc.CallOption) (*HelpResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(HelpResp)
	err := c.cc.Invoke(ctx, UtilityService_Help_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *utilityServiceClient) Ping(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (*PingResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(PingResp)
	err := c.cc.Invoke(ctx, UtilityService_Ping_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UtilityServiceServer is the server API for UtilityService service.
// All implementations must embed UnimplementedUtilityServiceServer
// for forward compatibility.
type UtilityServiceServer interface {
	HealthCheck(context.Context, *emptypb.Empty) (*HealthCheckResp, error)
	Help(context.Context, *HelpReq) (*HelpResp, error)
	Ping(context.Context, *emptypb.Empty) (*PingResp, error)
	mustEmbedUnimplementedUtilityServiceServer()
}

// UnimplementedUtilityServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedUtilityServiceServer struct{}

func (UnimplementedUtilityServiceServer) HealthCheck(context.Context, *emptypb.Empty) (*HealthCheckResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method HealthCheck not implemented")
}
func (UnimplementedUtilityServiceServer) Help(context.Context, *HelpReq) (*HelpResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Help not implemented")
}
func (UnimplementedUtilityServiceServer) Ping(context.Context, *emptypb.Empty) (*PingResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Ping not implemented")
}
func (UnimplementedUtilityServiceServer) mustEmbedUnimplementedUtilityServiceServer() {}
func (UnimplementedUtilityServiceServer) testEmbeddedByValue()                        {}

// UnsafeUtilityServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to UtilityServiceServer will
// result in compilation errors.
type UnsafeUtilityServiceServer interface {
	mustEmbedUnimplementedUtilityServiceServer()
}

func RegisterUtilityServiceServer(s grpc.ServiceRegistrar, srv UtilityServiceServer) {
	// If the following call pancis, it indicates UnimplementedUtilityServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&UtilityService_ServiceDesc, srv)
}

func _UtilityService_HealthCheck_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(UtilityServiceServer).HealthCheck(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: UtilityService_HealthCheck_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(UtilityServiceServer).HealthCheck(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _UtilityService_Help_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(HelpReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(UtilityServiceServer).Help(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: UtilityService_Help_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(UtilityServiceServer).Help(ctx, req.(*HelpReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _UtilityService_Ping_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(UtilityServiceServer).Ping(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: UtilityService_Ping_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(UtilityServiceServer).Ping(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

// UtilityService_ServiceDesc is the grpc.ServiceDesc for UtilityService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var UtilityService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ginbot.proto.UtilityService",
	HandlerType: (*UtilityServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "HealthCheck",
			Handler:    _UtilityService_HealthCheck_Handler,
		},
		{
			MethodName: "Help",
			Handler:    _UtilityService_Help_Handler,
		},
		{
			MethodName: "Ping",
			Handler:    _UtilityService_Ping_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "ginbot/proto/utility.proto",
}
