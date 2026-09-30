# bandwidth-quota-service

Small gRPC service that backs Envoy's distributed bandwidth limit. Hosts the
authoritative token-bucket math behind a stable gRPC API; storage backend is
Redis (managed via Lua for atomicity).

For the static Envoy demo in the dedicated `bandwidth-testing` kind cluster,
see [demo/kind/README.md](demo/kind/README.md). It measures one, two and three
Envoys sharing the same TCP bandwidth budget, plus fail-closed recovery.

The [Helm chart](helm-chart/README.md) deploys the quota service with bundled
Redis, optional persistent storage, or an external Redis endpoint.

## API

```proto
service BandwidthQuotaService {
  rpc AcquireLease(AcquireLeaseRequest) returns (AcquireLeaseResponse);
}
```

See `proto/bandwidth/v1/quota.proto` for the full schema.
The server also registers the wire-compatible service name
`envoy.extensions.distributed_token_bucket.v3.BandwidthQuotaService` used by the
experimental Envoy branch, whose protobuf package differs from this service's.

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

- Each bucket stores a key, token balance and timestamp. Measure actual memory
  with Redis `MEMORY USAGE`; key length, encoding and allocator overhead matter.
- With full grants, fleet-wide acquisition QPS is roughly
  `global_rate_kib_per_sec / lease_kib`: a shared 1 MiB/s rate and 64 KiB lease
  needs about 16 full grants/sec/bucket. Empty and partial grants add requests;
  actual QPS also depends on replica count and retry cadence.
- The service itself is stateless; horizontal scaling is just spinning up
  more replicas behind your load balancer of choice.
