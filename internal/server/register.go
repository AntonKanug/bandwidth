package server

import (
	"context"

	bandwidthv1 "github.com/antonk/bandwidth-quota-service/gen/go/bandwidth/v1"
	"google.golang.org/grpc"
)

// EnvoyServiceName is the service name used by the experimental Envoy filter
// on fork/antonk/distrib-bandwidth. Its request and response field numbers and
// types match bandwidth.v1, but its protobuf package (and RPC path) differs.
const EnvoyServiceName = "envoy.extensions.distributed_token_bucket.v3.BandwidthQuotaService"

// Register serves both the public v1 API and the experimental Envoy RPC path.
// Keep this compatibility adapter until the two projects use one proto schema.
func Register(registrar grpc.ServiceRegistrar, srv bandwidthv1.BandwidthQuotaServiceServer) {
	bandwidthv1.RegisterBandwidthQuotaServiceServer(registrar, srv)
	registrar.RegisterService(&grpc.ServiceDesc{
		ServiceName: EnvoyServiceName,
		HandlerType: (*bandwidthv1.BandwidthQuotaServiceServer)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "AcquireLease",
			Handler: func(service interface{}, ctx context.Context, decode func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				req := new(bandwidthv1.AcquireLeaseRequest)
				if err := decode(req); err != nil {
					return nil, err
				}
				handler := func(ctx context.Context, request interface{}) (interface{}, error) {
					return service.(bandwidthv1.BandwidthQuotaServiceServer).AcquireLease(ctx, request.(*bandwidthv1.AcquireLeaseRequest))
				}
				if interceptor == nil {
					return handler(ctx, req)
				}
				return interceptor(ctx, req, &grpc.UnaryServerInfo{
					Server:     service,
					FullMethod: "/" + EnvoyServiceName + "/AcquireLease",
				}, handler)
			},
		}},
	}, srv)
}
