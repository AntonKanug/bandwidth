package server

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bandwidthv1 "github.com/antonk/bandwidth-quota-service/gen/go/bandwidth/v1"
)

// Scripter is the subset of go-redis surface we need to invoke a cached
// Lua script. *redis.Client satisfies this; tests substitute a miniredis
// client wired to the same interface.
type Scripter interface {
	redis.Scripter
}

// Server implements bandwidth.v1.BandwidthQuotaService.
type Server struct {
	bandwidthv1.UnimplementedBandwidthQuotaServiceServer

	scripter Scripter
	script   *redis.Script
}

// New builds a Server backed by `s`. The Lua script is registered with
// go-redis' Script helper, which handles SCRIPT LOAD on first use, EVALSHA
// thereafter, and transparent NOSCRIPT recovery — no manual SHA caching, no
// permanent error caching.
func New(s Scripter) *Server {
	return &Server{
		scripter: s,
		script:   redis.NewScript(LuaTokenBucket),
	}
}

// AcquireLease atomically grants up to `RequestedTokens` from the bucket
// identified by `Key` at rate `RateTokensPerSec`.
func (s *Server) AcquireLease(
	ctx context.Context, req *bandwidthv1.AcquireLeaseRequest,
) (*bandwidthv1.AcquireLeaseResponse, error) {
	if req.GetKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}
	if req.GetRateTokensPerSec() == 0 {
		return nil, status.Error(codes.InvalidArgument, "rate_tokens_per_sec must be > 0")
	}
	if req.GetRequestedTokens() == 0 {
		// Trivially satisfiable; skip the round trip.
		return &bandwidthv1.AcquireLeaseResponse{GrantedTokens: 0}, nil
	}

	keys := []string{req.GetKey()}
	args := []interface{}{req.GetRateTokensPerSec(), req.GetRequestedTokens()}

	// Run() does EVALSHA then falls back to EVAL on NOSCRIPT, automatically
	// updating its cached hash on the way. No sticky error state.
	result, err := s.script.Run(ctx, s.scripter, keys, args...).Result()
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "redis eval failed: %v", err)
	}

	granted, err := toUint64(result)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unexpected reply type: %v", err)
	}

	resp := &bandwidthv1.AcquireLeaseResponse{GrantedTokens: granted}
	if granted == 0 {
		// Suggest the caller wait roughly the time it takes to refill at least
		// one byte. If the bucket is currently empty and refills at `rate`/sec,
		// one token arrives in 1000/rate ms. Clamp to 1 ms minimum so callers
		// don't spin if the rate is very high.
		if req.GetRateTokensPerSec() > 0 {
			retry := time.Second / time.Duration(req.GetRateTokensPerSec())
			if retry < time.Millisecond {
				retry = time.Millisecond
			}
			resp.RetryAfterMs = uint64(retry / time.Millisecond)
		}
	}
	return resp, nil
}

// toUint64 normalizes the integer-shaped Redis replies that go-redis surfaces
// (int64 from EVAL/EVALSHA; some hosts/versions return strings for large
// integers — defensive coverage).
func toUint64(v interface{}) (uint64, error) {
	switch t := v.(type) {
	case int64:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	case int:
		if t < 0 {
			return 0, nil
		}
		return uint64(t), nil
	case string:
		var n uint64
		if _, err := fmt.Sscanf(t, "%d", &n); err != nil {
			return 0, fmt.Errorf("parse %q: %w", t, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unsupported reply type %T", v)
	}
}

// NewRedisServer wires the gRPC Server against a fresh *redis.Client at addr.
func NewRedisServer(addr string) *Server {
	return New(redis.NewClient(&redis.Options{Addr: addr}))
}
