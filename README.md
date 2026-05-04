# bandwidth-quota-service

Small gRPC service that backs Envoy's distributed bandwidth limit. Hosts the
authoritative token-bucket math behind a stable gRPC API; storage backend is
Redis (managed via Lua for atomicity).

## API

```proto
service BandwidthQuotaService {
  rpc AcquireLease(AcquireLeaseRequest) returns (AcquireLeaseResponse);
}
```

See `proto/bandwidth/v1/quota.proto` for the full schema.

## Build and run locally

```bash
# 1. Generate Go bindings from the proto.
brew install bufbuild/buf/buf   # one-time
make proto
go mod tidy

# 2. Run a local Redis.
docker run -d --name redis -p 6379:6379 redis:7

# 3. Build and run the service.
make run                         # listens on :9300, talks to redis://127.0.0.1:6379
```

## Tests

```bash
make test
```

Tests use [`miniredis`](https://github.com/alicebob/miniredis) so no external
Redis is required. Cases cover fresh-bucket grants, capacity clamping,
time-based refill, empty-bucket retry hints, shared-key contention and
NOSCRIPT recovery.

## Docker

```bash
make docker
docker run --rm -p 9300:9300 --link redis bandwidth-quota:dev \
  --listen=:9300 --redis-addr=redis:6379
```

## How Envoy talks to this

Envoy's `tcp_bandwidth_limit` filter, when configured with `distributed:`,
uses `Grpc::AsyncClientManager` to issue `AcquireLease` to this service
whenever its local lease drops below the configured low watermark. The local
lease is then drained synchronously inside Envoy's worker against per-byte
read/write traffic.

See the Envoy fork's docs:
`docs/root/configuration/listeners/network_filters/tcp_bandwidth_limit_filter.rst`.

## Design notes

- **Atomicity.** The bucket math runs as a single Lua script under `EVALSHA`,
  so two callers updating the same key cannot race a lost-update. Redis is
  single-threaded for command processing, which is why this is sufficient.
- **Clock.** `redis.call('TIME')` is used server-side instead of trusting the
  caller's clock. Eliminates clock-skew bugs across N Envoys.
- **Partial grants.** `requested_tokens` is a *maximum*; the service returns
  whatever was available in `[0, requested_tokens]`. Callers (the Envoy
  bucket) drain the granted amount before fetching more.
- **TTL.** Each bucket key carries a 60-second sliding TTL refreshed on every
  call. Idle buckets evict; the next call recreates them at full capacity.
- **Stateless.** Multiple replicas are safe; the bucket state lives in Redis,
  not in the service.

## Operational sizing

- One bucket key in Redis is ~100 bytes. A million buckets is ~100 MB.
- Steady-state Redis QPS per Envoy is roughly `rate_kbps / lease_kb`. With a
  1 MiB/s rate and 64 KiB lease, that's ~16 calls/sec/Envoy/bucket.
- The service itself is stateless; horizontal scaling is just spinning up
  more replicas behind your load balancer of choice.
