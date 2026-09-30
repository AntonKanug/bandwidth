# Verified kind demo — September 30, 2026

All seven static-config checks passed in `kind-bandwidth-testing` at
19:43 UTC. The configured aggregate budget was **1024 KiB/s per direction**.

| Check | Measured aggregate KiB/s | Result |
| --- | ---: | --- |
| One Envoy, download | 1035.39 | Pass |
| Two Envoys, download | 1024.66 | Pass |
| Two Envoys, upload | 1023.98 | Pass |
| Three Envoys, download | 1025.73 | Pass |
| One active Envoy among three | 1025.17 | Pass |
| Quota service stopped, fail-closed | 0.00 | Pass |
| Quota service restored | 1025.27 | Pass |

The shared cap holds as the Envoy count changes, and an active Envoy can use the
full budget while its peers are idle. Individual shares are unequal: the three
download streams measured 595.35, 181.25 and 249.14 KiB/s. This implementation
does not guarantee equal fairness.

Measurements used receiver-confirmed bytes, a three-second warmup, and a
twenty-second observation window (six seconds for the outage). The Redis bucket
allows an initial one-second burst and Envoy caches leases, so short windows can
exceed the configured average slightly. The test tolerance was 25%.

The tested binary is Envoy `1.40.0-dev`, built from upstream main
`f15dbecafda74aab1c88209bb59a0428e8e30e87` plus the TCP-only distributed bandwidth
port on `codex/distributed-tcp-bandwidth`. [build-envoy.patch](build-envoy.patch)
preserves the port. The kind cluster is ARM64 Kubernetes 1.36.1 on one node;
each Envoy runs in its own pod with one worker. Two quota replicas use one Redis
instance. ECP is not deployed.

The first upload run was affected by concurrent C++ test compilation reaching
its 6 GiB memory limit: quota RPCs timed out and the fail-closed limiter stalled.
After stopping compilation, the unchanged upload generator measured 1023.98
KiB/s. The table above uses the complete rerun without that contention. Run
heavy builds separately from throughput measurements on this Docker VM.

The verifier restored the demo to two healthy Envoy pods and two quota replicas.
`envoy-2` was removed after the three-Envoy checks.

## Code validation

All four focused Envoy C++ targets passed: **68 tests, zero failures or skips**.

| Target | Tests |
| --- | ---: |
| `distributed_token_bucket_test` | 16 |
| `grpc_lease_fetcher_test` | 5 |
| TCP bandwidth `filter_test` | 39 |
| TCP bandwidth `config_test` | 8 |

The test summary, XML and logs are retained locally under
`/private/tmp/bandwidth-envoy-build/output/cpp-test-results/`. The full Envoy
suite, sanitizers and coverage were not run. Formatting, whitespace and exported
patch consistency checks passed. The dedicated build container is stopped;
the build cache is preserved.

The Go quota service passed `go test -race ./...` and `go vet ./...`, including
gRPC service-name compatibility and fractional refill regression checks.

Raw reports are retained locally and excluded from Git: `results/report.json`
contains per-Envoy rates and one-second samples;
`results/initial-upload-during-build.json` preserves the initial upload result
during build contention. Both paths are relative to this directory.

- [Reproduction commands](README.md)
- [EgressRoute source trace and production gaps](ASSESSMENT.md)
