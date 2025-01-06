// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v5.29.2
// source: proto/entertainment.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	EntertainmentService_GetRandomNumber_FullMethodName = "/ginbot.entertainment.EntertainmentService/GetRandomNumber"
	EntertainmentService_SetBirthday_FullMethodName     = "/ginbot.entertainment.EntertainmentService/SetBirthday"
)

// EntertainmentServiceClient is the client API for EntertainmentService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type EntertainmentServiceClient interface {
	GetRandomNumber(ctx context.Context, in *GetRandomNumberReq, opts ...grpc.CallOption) (*GetRandomNumberResp, error)
	SetBirthday(ctx context.Context, in *SetBirthdayReq, opts ...grpc.CallOption) (*SetBirthdayResp, error)
}

type entertainmentServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewEntertainmentServiceClient(cc grpc.ClientConnInterface) EntertainmentServiceClient {
	return &entertainmentServiceClient{cc}
}

func (c *entertainmentServiceClient) GetRandomNumber(ctx context.Context, in *GetRandomNumberReq, opts ...grpc.CallOption) (*GetRandomNumberResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetRandomNumberResp)
	err := c.cc.Invoke(ctx, EntertainmentService_GetRandomNumber_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *entertainmentServiceClient) SetBirthday(ctx context.Context, in *SetBirthdayReq, opts ...grpc.CallOption) (*SetBirthdayResp, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(SetBirthdayResp)
	err := c.cc.Invoke(ctx, EntertainmentService_SetBirthday_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EntertainmentServiceServer is the server API for EntertainmentService service.
// All implementations must embed UnimplementedEntertainmentServiceServer
// for forward compatibility.
type EntertainmentServiceServer interface {
	GetRandomNumber(context.Context, *GetRandomNumberReq) (*GetRandomNumberResp, error)
	SetBirthday(context.Context, *SetBirthdayReq) (*SetBirthdayResp, error)
	mustEmbedUnimplementedEntertainmentServiceServer()
}

// UnimplementedEntertainmentServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedEntertainmentServiceServer struct{}

func (UnimplementedEntertainmentServiceServer) GetRandomNumber(context.Context, *GetRandomNumberReq) (*GetRandomNumberResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetRandomNumber not implemented")
}
func (UnimplementedEntertainmentServiceServer) SetBirthday(context.Context, *SetBirthdayReq) (*SetBirthdayResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetBirthday not implemented")
}
func (UnimplementedEntertainmentServiceServer) mustEmbedUnimplementedEntertainmentServiceServer() {}
func (UnimplementedEntertainmentServiceServer) testEmbeddedByValue()                              {}

// UnsafeEntertainmentServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to EntertainmentServiceServer will
// result in compilation errors.
type UnsafeEntertainmentServiceServer interface {
	mustEmbedUnimplementedEntertainmentServiceServer()
}

func RegisterEntertainmentServiceServer(s grpc.ServiceRegistrar, srv EntertainmentServiceServer) {
	// If the following call pancis, it indicates UnimplementedEntertainmentServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&EntertainmentService_ServiceDesc, srv)
}

func _EntertainmentService_GetRandomNumber_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetRandomNumberReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(EntertainmentServiceServer).GetRandomNumber(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: EntertainmentService_GetRandomNumber_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(EntertainmentServiceServer).GetRandomNumber(ctx, req.(*GetRandomNumberReq))
	}
	return interceptor(ctx, in, info, handler)
}

func _EntertainmentService_SetBirthday_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SetBirthdayReq)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(EntertainmentServiceServer).SetBirthday(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: EntertainmentService_SetBirthday_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(EntertainmentServiceServer).SetBirthday(ctx, req.(*SetBirthdayReq))
	}
	return interceptor(ctx, in, info, handler)
}

// EntertainmentService_ServiceDesc is the grpc.ServiceDesc for EntertainmentService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var EntertainmentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ginbot.entertainment.EntertainmentService",
	HandlerType: (*EntertainmentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetRandomNumber",
			Handler:    _EntertainmentService_GetRandomNumber_Handler,
		},
		{
			MethodName: "SetBirthday",
			Handler:    _EntertainmentService_SetBirthday_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "proto/entertainment.proto",
}
