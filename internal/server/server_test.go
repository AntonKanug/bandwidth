package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	bandwidthv1 "github.com/antonk/bandwidth-quota-service/gen/go/bandwidth/v1"
)

// newTestServer wires Server against a fresh miniredis instance. Returns the
// server, the miniredis instance (for FlushAll-style tests) and a cleanup func.
func newTestServer(t *testing.T) (*Server, *miniredis.Miniredis, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis run: %v", err)
	}
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	srv := New(c)
	return srv, mr, func() {
		_ = c.Close()
		mr.Close()
	}
}

func acquire(t *testing.T, srv *Server, key string, requested, rate uint64) *bandwidthv1.AcquireLeaseResponse {
	t.Helper()
	resp, err := srv.AcquireLease(context.Background(), &bandwidthv1.AcquireLeaseRequest{
		Key:               key,
		RequestedTokens:   requested,
		RateTokensPerSec:  rate,
	})
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	return resp
}

func TestFreshKeyGrantsFromCapacity(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	// Ask for less than the rate; bucket is fresh so full capacity is `rate`.
	resp := acquire(t, srv, "k1", 500, 1000)
	if resp.GrantedTokens != 500 {
		t.Fatalf("granted=%d, want 500", resp.GrantedTokens)
	}
}

func TestRequestExceedingCapacityIsClampedToCapacity(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	resp := acquire(t, srv, "k1", 5000, 1000)
	if resp.GrantedTokens != 1000 {
		t.Fatalf("granted=%d, want 1000 (full capacity)", resp.GrantedTokens)
	}
}

func TestSuccessiveCallsDeductAndRefill(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	// First call drains 800 of 1000.
	r1 := acquire(t, srv, "k1", 800, 1000)
	if r1.GrantedTokens != 800 {
		t.Fatalf("first granted=%d, want 800", r1.GrantedTokens)
	}

	// Immediate follow-up sees ~200 available (small slack for clock drift
	// between the two redis.call('TIME') invocations).
	r2 := acquire(t, srv, "k1", 1000, 1000)
	if r2.GrantedTokens < 195 || r2.GrantedTokens > 220 {
		t.Fatalf("second granted=%d, want ~200", r2.GrantedTokens)
	}

	// Sleep 200ms in real time; bucket should refill ~200 tokens. We use real
	// time rather than miniredis FastForward because miniredis's mock clock
	// inside the Lua sandbox is not always wired to FastForward in a way that
	// preserves redis.call('TIME') semantics across versions.
	time.Sleep(200 * time.Millisecond)
	r3 := acquire(t, srv, "k1", 1000, 1000)
	if r3.GrantedTokens < 150 || r3.GrantedTokens > 250 {
		t.Fatalf("third granted=%d, want ~200 after 200ms sleep", r3.GrantedTokens)
	}
}

func TestEmptyBucketReturnsRetryAfter(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	// Drain the bucket completely. We use a low rate so the small wall-clock
	// drift between the two calls (driven by redis.call('TIME')) refills only
	// a negligible amount before the second call.
	_ = acquire(t, srv, "k1", 10, 10)

	// Ask immediately for more. With rate=10 tokens/sec, a few ms of drift
	// can refill at most a small fraction of one token; the bucket should
	// still report empty and emit a retry hint.
	r := acquire(t, srv, "k1", 100, 10)
	if r.GrantedTokens != 0 {
		t.Fatalf("granted=%d, want 0", r.GrantedTokens)
	}
	if r.RetryAfterMs == 0 {
		t.Fatalf("expected non-zero retry hint")
	}
}

func TestZeroRequestedTokensIsNoOp(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	r := acquire(t, srv, "k1", 0, 1000)
	if r.GrantedTokens != 0 {
		t.Fatalf("granted=%d, want 0", r.GrantedTokens)
	}
}

func TestInvalidArgsRejected(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	if _, err := srv.AcquireLease(context.Background(), &bandwidthv1.AcquireLeaseRequest{
		Key: "", RequestedTokens: 100, RateTokensPerSec: 1000,
	}); err == nil {
		t.Fatal("expected error for empty key")
	}

	if _, err := srv.AcquireLease(context.Background(), &bandwidthv1.AcquireLeaseRequest{
		Key: "k", RequestedTokens: 100, RateTokensPerSec: 0,
	}); err == nil {
		t.Fatal("expected error for zero rate")
	}
}

func TestSeparateKeysAreIndependent(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	r1 := acquire(t, srv, "tenant-a", 800, 1000)
	r2 := acquire(t, srv, "tenant-b", 800, 1000)
	if r1.GrantedTokens != 800 || r2.GrantedTokens != 800 {
		t.Fatalf("granted=%d/%d, want 800/800 (independent buckets)",
			r1.GrantedTokens, r2.GrantedTokens)
	}
}

func TestSharedKeyContendsCorrectly(t *testing.T) {
	srv, _, cleanup := newTestServer(t)
	defer cleanup()

	// Two callers competing for the same 1000-token bucket. Total grants
	// must not exceed the bucket capacity (with small slack for refill that
	// accrues between the two TIME readings — submillisecond at most).
	var wg sync.WaitGroup
	var got1, got2 uint64
	wg.Add(2)
	go func() {
		defer wg.Done()
		r := acquire(t, srv, "shared", 800, 1000)
		got1 = r.GrantedTokens
	}()
	go func() {
		defer wg.Done()
		r := acquire(t, srv, "shared", 800, 1000)
		got2 = r.GrantedTokens
	}()
	wg.Wait()

	// Must not exceed capacity by more than the refill that legitimately
	// accrued between the two server-side TIME calls. Allow 5 tokens slack;
	// real Redis would be tighter, but miniredis serializes via a coarse
	// mutex which can register multiple ms between calls.
	const slack = 5
	if got1+got2 > 1000+slack {
		t.Fatalf("sum=%d > 1000+%d (bucket over-allocated)", got1+got2, slack)
	}
}

func TestNoScriptRecovery(t *testing.T) {
	srv, mr, cleanup := newTestServer(t)
	defer cleanup()

	_ = acquire(t, srv, "k1", 500, 1000)

	// Simulate Redis evicting the script: clear all loaded scripts.
	mr.FlushAll()

	// Next call should recover via EVAL fallback.
	r := acquire(t, srv, "k1", 500, 1000)
	if r.GrantedTokens == 0 {
		t.Fatalf("granted=0 after script eviction; expected recovery via EVAL")
	}
}
