# Distributed TCP bandwidth experiment

The kind demo uses **static Envoy configuration**. It runs neither ECP nor the
production EgressRoute CRD. The test asks whether one upload budget and one
download budget can be shared across N Envoy processes through a quota service
backed by one Redis instance. It does not establish production readiness.

## Where route bandwidth comes from

| Stage | Current behavior | Source |
| --- | --- | --- |
| Policy API | Create/request/patch accept optional `bandwidth.downloadKiBs` and `bandwidth.uploadKiBs`. | API fields: `connectivity-config/connectivity-config-api/src/main/conjure/network-policy/network-policy-api.yml:36`, bandwidth type: `connectivity-config/connectivity-config-api/src/main/conjure/network-policy/network-policy-objects.yml:66` |
| Stored policy | Creation copies the requested bandwidth directly; patch changes it only when supplied. | create: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/NetworkPolicyResource.java:215`, patch: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/NetworkPolicyResource.java:448` |
| Route conversion | Copies bandwidth without dividing by Envoy count. Names the route `egress-route-<policy RID locator>`. | converter: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/NetworkPolicyEgressRouteConverter.java:62`, identity: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/NetworkPolicyEgressRouteConverter.java:101` |
| Edge Kubernetes sync | Copies both directions into `EgressRoute.spec.bandwidth`; writes the configured services namespace. | edge conversion: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/sync/EdgeEgressSyncTask.java:356`, writer: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/sync/EgressRouteSyncer.java:28` |
| ECP ingestion | Route changes request new snapshots. Routes must be referenced by a PlatformEgressSecurityGroup to enter the snapshot. | watcher: `egress-control-plane/server/server.go:318`, selection: `egress-control-plane/managers/cache/egress_envoy.go:185` |
| ECP filter | Download means Envoy **write to the client**; upload means Envoy **read from the client**. The timer defaults to 50 ms. | mapping: `egress-control-plane/pkg/envoy/nodes/filters/tcp_bandwidth_limit_filter.go:50` |
| Current deployment gate | Per-route bandwidth filters are emitted only for single-node deployments. Multi-node snapshots omit them. | HTTP gate: `egress-control-plane/managers/cache/computers/egress_listener.go:88`, TCP gate: `egress-control-plane/managers/cache/computers/egress_listener.go:109` |

There is no inherited default bandwidth in this path. ECP treats absent or
nonpositive directional values as unlimited; if both are nonpositive it emits
no filter (conversion: `egress-control-plane/pkg/envoy/nodes/filters/tcp_bandwidth_limit_filter.go:111`).
This differs from the Envoy proto itself, where a present zero means blocked
(proto: `egress-control-plane/vendor/github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_bandwidth_limit/v3/tcp_bandwidth_limit.pb.go:34`).
The demo deliberately uses positive values.

## What the static demo represents

[generate.py](generate.py#L56) maps `--rate-kib`
to both read and write limits. Every Envoy receives the same key,
`kind/bandwidth-testing/<namespace>/demo-policy`, and 64 KiB leases with
`fail_open: false`. The experimental filter adds separate `:read` and `:write`
suffixes. One worker per Envoy keeps the first experiment bounded; two quota
replicas share Redis. Redis is ephemeral and has no failover in this demo.

[traffic.py](traffic.py#L45) counts received
download bytes and upstream-acknowledged upload bytes, rather than queued writes.
[verify.py](verify.py#L49) exercises one, two,
and three Envoys, one active Envoy among three, and an optional quota outage and
recovery. Measurements exclude a three-second warmup and use a 25% throughput
tolerance. These checks assess aggregate throughput, not equal per-Envoy shares
or a strict cap in every short time window. Recorded results are the evidence
for which checks actually completed.

## Production gaps

1. **Policy attribution.** Existing ECP chains select on destination CIDR/SNI/port,
   before authorization (chain: `egress-control-plane/managers/cache/computers/egress_listener.go:189`).
   Cloud policies can share a destination across enrollments; edge instead rejects
   duplicate address/port policies (edge rule: `connectivity-config/connectivity-config/src/main/java/com/palantir/connectivityconfig/networkpolicy/NetworkPolicyResource.java:1092`).
   Independent policy budgets need authenticated selected-policy attribution from
   EAF, or a deliberate destination-shared scope. A namespaced policy key alone
   cannot distinguish traffic that selected the same destination-only chain.
   ECP's existing EAF-metadata-to-filter-state path is a useful integration pattern
   (filter state: `egress-control-plane/managers/cache/computers/egress_listener.go:626`).
2. **Published Envoy and API integration.** Current ECP's generated TCP limiter
   type lacks `distributed` (type: `egress-control-plane/vendor/github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_bandwidth_limit/v3/tcp_bandwidth_limit.pb.go:29`).
   The demo requires the experimental Envoy image, not stock Envoy. Production
   needs matching published C++ and Go protos, an egress-envoy image dependency,
   quota-cluster discovery, and an explicit rollout gate replacing the single-node
   restriction. The temporary [RPC adapter](../../internal/server/register.go#L10)
   supports the fork's protobuf service name as well as `bandwidth.v1`; the wire
   fields match today, but these schemas should have one versioned authority.
3. **Rate ownership and updates.** Each request currently supplies the rate;
   [Lua](../../internal/server/lua.go#L18) stores only tokens and
   timestamp. Mixed old/new rates during rollout therefore change refill and
   capacity per caller. Persist authoritative policy rate/version, validate callers,
   and define transitions before exposing this service beyond the experiment.
4. **Lease guarantees.** Grants reserve bytes before actual transmission. Local
   unused credits, process restarts, listener replacement, and idle leases require
   explicit burst and accounting semantics. Measure the bound as fleet size and
   active filter configurations grow; aggregate average throughput does not prove
   an instantaneous cap or fairness. Fail-open would weaken the shared guarantee.
5. **Service operations.** Redis availability, failover/recovery semantics, durable
   policy state, authentication, numeric bounds, and dependency-aware readiness
   remain work. The demo's gRPC readiness only checks the listening socket; it does
   not establish Redis health. Test Redis failure and existing-stream recovery
   separately from stopping all quota replicas.
6. **Native filter hardening.** The original experimental branch ignored
   `retry_after_ms`, retried immediately after failed acquisitions in fail-closed
   mode, and stayed fail-open on healthy zero grants. The port onto September 30
   upstream main fixes failure backoff and zero-grant recovery with regression
   tests; the retry hint is still ignored. Its asynchronous callbacks use an
   atomic alive flag without owning the object;
   teardown across workers and the main dispatcher needs a stronger lifetime
   contract. The static demo uses fail-closed and
   does not exercise listener replacement. Source: the distributed branch's
   `source/extensions/common/distributed_token_bucket/{distributed_token_bucket,grpc_lease_fetcher}.cc`.

The existing policy bandwidth fields are suitable inputs for a shared budget.
The main missing production decisions are which authenticated policy owns each
connection and what guarantees apply to leases during updates and failures.
