package server

import (
	"context"
	"net"
	"testing"
	"time"

	bandwidthv1 "github.com/antonk/bandwidth-quota-service/gen/go/bandwidth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestRegisterServesBothRPCNamesAndSharesQuota(t *testing.T) {
	for _, intercept := range []bool{false, true} {
		name := "without-interceptor"
		if intercept {
			name = "with-interceptor"
		}
		t.Run(name, func(t *testing.T) {
			srv, mr, cleanup := newTestServer(t)
			defer cleanup()
			mr.SetTime(time.Unix(1700000000, 0))
			methods := make(chan string, 2)
			var options []grpc.ServerOption
			if intercept {
				options = append(options, grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
					methods <- info.FullMethod
					return handler(ctx, req)
				}))
			}
			gs := grpc.NewServer(options...)
			Register(gs, srv)
			listener := bufconn.Listen(1024 * 1024)
			defer listener.Close()
			defer gs.Stop()
			go gs.Serve(listener)
			conn, err := grpc.NewClient("passthrough:///quota",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for i, method := range []string{
				bandwidthv1.BandwidthQuotaService_AcquireLease_FullMethodName,
				"/" + EnvoyServiceName + "/AcquireLease",
			} {
				resp := new(bandwidthv1.AcquireLeaseResponse)
				err := conn.Invoke(ctx, method, &bandwidthv1.AcquireLeaseRequest{
					Key: "shared", RequestedTokens: 800, RateTokensPerSec: 1000,
				}, resp)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				want := []uint64{800, 200}[i]
				if resp.GrantedTokens != want {
					t.Fatalf("%s: granted %d, want %d", method, resp.GrantedTokens, want)
				}
				if intercept {
					if got := <-methods; got != method {
						t.Fatalf("interceptor method %q, want %q", got, method)
					}
				}
			}
		})
	}
}
