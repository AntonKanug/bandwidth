# Historical local testing draft

Use [the current kind demo](demo/kind/README.md) for the static-config experiment
in `bandwidth-testing`, receiver-confirmed throughput measurements, and saved
results. The instructions below predate the verified gRPC service-name adapter;
their paths and assumptions are historical, not evidence that those checks ran.

# End-to-end testing — bandwidth-quota service + Envoy

This walks you from zero to a running fleet of two Envoys sharing a single
bandwidth limit through the gRPC quota service. ~15 minutes once Envoy is
built. Three terminal panes recommended.

## 0. Prerequisites

```bash
which redis-cli docker go bazel
ls -d /Users/antonk/envoy /Users/antonk/bandwidth-quota-service
```

If `redis-server` isn't installed locally, you'll use Docker for it. The
service builds quickly; Envoy needs one initial bazel build (~10 min cold).

## 1. Start Redis

```bash
docker run -d --name bw-redis -p 6379:6379 redis:7
redis-cli ping     # → PONG
```

To clear bucket state between runs:

```bash
redis-cli KEYS 'tenant.*' | xargs -r redis-cli DEL
```

## 2. Start the bandwidth-quota service

```bash
cd /Users/antonk/bandwidth-quota-service
make run
# logs: "bandwidth-quota listening on :9300, redis=127.0.0.1:6379"
```

Quick sanity check from another shell (`brew install grpcurl` if needed):

```bash
grpcurl -plaintext -import-path proto -proto proto/bandwidth/v1/quota.proto \
  -d '{"key":"smoke:read","requested_tokens":1024,"rate_tokens_per_sec":1048576}' \
  localhost:9300 bandwidth.v1.BandwidthQuotaService/AcquireLease
# → {"grantedTokens":"1024"}
```

## 3. Build envoy-static

```bash
cd /Users/antonk/envoy
bazel build --macos_minimum_os=10.15 //source/exe:envoy-static
```

Binary lands at `./bazel-bin/source/exe/envoy-static`.

## 4. Envoy configs

The reference configs live at:

- `examples/envoy-bw-a.yaml` — Envoy A on listener `:9000`, admin `:9901`
- `examples/envoy-bw-b.yaml` — Envoy B on listener `:9001`, admin `:9902`

Both point at the same `bandwidth_quota` cluster (the service on `:9300`)
and the same `key: tenant.acme.bandwidth` — that's what makes the rate
shared.

```bash
# Validate both configs first:
./bazel-bin/source/exe/envoy-static --mode validate -c examples/envoy-bw-a.yaml
./bazel-bin/source/exe/envoy-static --mode validate -c examples/envoy-bw-b.yaml
```

If you see `tcp_bandwidth_limit: distributed mode quota_service is
misconfigured`, the service isn't reachable on `:9300` — go back to step 2.

## 5. Start a sink upstream

```bash
# Discards everything sent to :8000.
python3 -c "
import socket
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', 8000))
s.listen()
while True:
    c, _ = s.accept()
    while c.recv(65536):
        pass
" &
```

## 6. Start both Envoys

```bash
# Terminal A
cd /Users/antonk/envoy
./bazel-bin/source/exe/envoy-static \
  -c /Users/antonk/bandwidth-quota-service/examples/envoy-bw-a.yaml \
  --base-id 1 --log-level info

# Terminal B
cd /Users/antonk/envoy
./bazel-bin/source/exe/envoy-static \
  -c /Users/antonk/bandwidth-quota-service/examples/envoy-bw-b.yaml \
  --base-id 2 --log-level info
```

Both should come up clean; the service should log new gRPC connections.

## 7. Drive traffic and observe

```bash
# Send 100 MiB through each Envoy in parallel.
( head -c 100M /dev/urandom | nc 127.0.0.1 9000 ) &
( head -c 100M /dev/urandom | nc 127.0.0.1 9001 ) &

# Watch the bucket drain in Redis (other shell):
watch -n 1 'redis-cli HGETALL tenant.acme.bandwidth:write'

# Watch each Envoy's stats:
curl -s localhost:9901/stats | grep -E 'bw\.(tcp_bandwidth_limit|distributed_bandwidth_limit)\.'
curl -s localhost:9902/stats | grep -E 'bw\.(tcp_bandwidth_limit|distributed_bandwidth_limit)\.'
```

**What to expect:**

- Combined throughput should converge to **~1 MiB/s total**, not 2 MiB/s.
  100 MiB through each takes ~200 seconds, not ~100.
- `bw.tcp_bandwidth_limit.write_rate_bps` summed across the two Envoys ≈
  1048576.
- `bw.distributed_bandwidth_limit.lease_requests_total` and
  `lease_granted_bytes_total` increase steadily.
- `bw.distributed_bandwidth_limit.fail_open_active = 0`.
- `redis-cli HGETALL tenant.acme.bandwidth:write` shows `tokens` oscillating
  while `ts` updates each fetch.

The B1 fix (read/write key suffixing) is verified by:

```bash
redis-cli KEYS 'tenant.*'
# → tenant.acme.bandwidth:read
# → tenant.acme.bandwidth:write
```

## 8. Verify fail-open behavior

While traffic is flowing:

```bash
docker stop bw-redis
curl -s localhost:9901/stats | grep fail_open_active
# → bw.distributed_bandwidth_limit.fail_open_active: 1
```

With `fail_open: false` (our default), traffic should **stop** flowing — the
filter buffers and backpressures, `nc` stalls.

If you flip the config to `fail_open: true` and add `fail_open_kbps: 100`,
each Envoy independently allows ~100 KiB/s during the outage. **Important:**
omitting `fail_open_kbps` while setting `fail_open: true` means each Envoy
falls back to the *global* rate per instance — N× the intended limit during
the outage. Always set `fail_open_kbps` explicitly if you turn fail-open on.

Restart Redis:

```bash
docker start bw-redis
# fail_open_active should drop back to 0 within ~1s; traffic resumes
```

## 9. Verify cross-Envoy fairness

```bash
redis-cli DEL tenant.acme.bandwidth:read tenant.acme.bandwidth:write

# Drain through Envoy A first
head -c 50M /dev/urandom | nc 127.0.0.1 9000 &
sleep 5
# Now Envoy B joins; bucket is partly drained
head -c 50M /dev/urandom | nc 127.0.0.1 9001 &
```

B's first lease grants whatever the bucket had to give — `lease_granted_bytes_total`
on B starts at zero and matches.

## 10. Cleanup

```bash
docker stop bw-redis && docker rm bw-redis
pkill -f envoy-static
pkill -f bandwidth-quota
```

---

## Common failure modes

| Symptom | Likely cause |
|---|---|
| `quota_service is misconfigured` at `--mode validate` | Service not on `:9300`; cluster name mismatch |
| Both Envoys serve at full rate (no shared limit) | Different `key` values, or different `quota_service` clusters |
| `lease_requests_total` increases but `lease_granted_bytes_total = 0` | Bucket empty server-side; check `redis-cli HGETALL tenant.acme.bandwidth:write` |
| Throughput much higher than configured | `lease_kb` set too large by hand; remove the override and let auto-derive run |
| Envoy logs reference `redis_lease_fetcher` | Stale binary; rebuild |

## Known limitations

These are real but not blockers for this version of the feature; the runbook
above won't catch them on its own. File issues / follow-up tasks for:

- **lease_kb floor bursty at small rates.** ``deriveLeaseKb`` floors at 64
  KiB. With a 1 MiB/s rate (the example), each Envoy holds ~512 ms of
  bandwidth in its local lease. Convergence to the global rate happens
  correctly but throughput is bursty across the two Envoys (one drains its
  lease, the other is empty, then they swap). Tune ``lease_kb`` down for
  smoother convergence at small rates, at the cost of higher service QPS.
- **Filter timer cadence under fail-closed.** While ``fail_open: false`` and
  the quota service is unreachable, the network filter's per-connection
  drain timer keeps re-arming at ``fill_interval``, so each throttled
  connection produces 20 timer fires/sec for the duration of the outage.
  Hot-loops a CPU but doesn't drop traffic; back-off on the timer is a
  follow-up.
- **Wire format frozen carries `rate_tokens_per_sec` on every call.** The
  v1 proto requires the rate on every ``AcquireLease`` request, with the
  semantics that "the first caller seeds the rate, subsequent calls are
  honored on a best-effort basis." This is a footgun frozen into the v1
  shape. Consider a separate ``ConfigureBucket`` RPC and ``idempotency_key``
  on AcquireLease before the proto stabilizes.

- **Long-idle bucket bursts.** The server-side key carries a 60s TTL refreshed
  on each call. If a key sits idle past the TTL it is evicted, and the next
  caller seeds a *full* bucket — a single capacity-worth of burst at the
  moment workload resumes. Tolerable for byte buckets sized at ~1s of rate.
- **Fail-open without `fail_open_kbps`.** With ``fail_open: true`` and no
  ``fail_open_kbps``, each Envoy independently allows the configured global
  rate during an outage, so the fleet aggregate is N× the intended limit.
  Always set ``fail_open_kbps`` (e.g. global / fleet_size) when enabling
  fail-open.
- **One quota service RPC in flight per fetcher.** Concurrent
  ``fetchLease`` calls coalesce; a second one before the first completes
  returns ``granted=0``. Fine for a single bucket per filter chain (the only
  case today). Not fine for fan-out across many keys per filter chain.
- **Pinned to the main dispatcher.** All ``AcquireLease`` traffic for an
  Envoy goes through one dispatcher. At very high lease QPS this can become
  a bottleneck. Per-worker dispatchers + per-worker fetcher state are the
  next step.
- **Lua bucket math relies on Redis preserving fractional tokens via
  ``HSET``.** The script writes back ``tokens`` as a string that may include
  a fractional part. ``tonumber`` round-trips that to a double. Anyone who
  "tightens" the script to use ``HINCRBY`` would break refill smoothness.
  Don't.
- **Bucket destruction race.** ``DistributedTokenBucket`` and
  ``GrpcLeaseFetcher`` use an ``alive_`` flag to make late callbacks safe.
  This handles the common case (Envoy filter config destruction on the main
  thread). It does not formally synchronize destruction against a callback
  *currently* executing on another thread; the practical safeguard is that
  ``FilterConfig`` is typically destroyed on the main thread by Envoy's
  config-update path.

## What this validates

- The TCP filter wires through to the gRPC service end-to-end.
- The Lua bucket actually shares state across two Envoys.
- **B1**: read/write key suffixing keeps directions independent.
- **B2**: `fail_open: false` actually stops traffic on outage.
- **B3**: fail-open recovery returns to normal mode without stalling.
- **D7**: stats are populated under `<stat_prefix>.distributed_bandwidth_limit.*`.

Server-side atomicity (B5: `redis.call('TIME')`, D1: `HSET`) is covered by
the in-tree miniredis tests under `internal/server/`.
