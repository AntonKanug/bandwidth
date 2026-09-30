# Bandwidth quota Helm chart

Deploys the quota service with **two replicas and one bundled Redis instance**.
All quota replicas use the same Redis bucket state. Envoy and ECP are configured
separately; see the [static Envoy demo](../demo/kind/README.md).

Requires Helm 3 or later and Kubernetes 1.27 or later (native gRPC probes).
The chart has no external chart dependencies.

## Install

Build and publish this repository's quota image to your registry, then install
with that repository and tag:

```bash
helm upgrade --install bandwidth ./helm-chart \
  --namespace bandwidth --create-namespace \
  --set image.repository=registry.example.com/team/bandwidth-quota \
  --set-string image.tag=YOUR_TAG \
  --wait --timeout 3m
```

The default image is `bandwidth-quota:dev`, matching `make docker`; there is no
published image assumed by this chart. Set `imagePullSecrets` for a private registry.

For the existing local kind cluster, load the image instead of publishing it:

```bash
make docker
kind load docker-image --name bandwidth-testing bandwidth-quota:dev
helm upgrade --install bandwidth ./helm-chart \
  --kubeconfig /private/tmp/bandwidth-testing.kubeconfig \
  --kube-context kind-bandwidth-testing \
  --namespace bandwidth-helm --create-namespace \
  --wait --timeout 3m
```

If the demo's `bandwidth-quota:testing` image is already loaded, skip the first
two commands and add `--set image.tag=testing` to the Helm command.

For release `bandwidth` in namespace `bandwidth`, the gRPC endpoint is
`bandwidth-bandwidth-quota.bandwidth.svc:9300`. `helm get notes bandwidth -n bandwidth`
prints the endpoint for your release. Configure the Envoy quota cluster with
HTTP/2 and this address. Envoys sharing a budget need the same `distributed.key`
and directional rates. Rates come from Envoy requests, not Helm values.

## Redis options

Bundled Redis uses an `emptyDir` by default. Enable persistence at installation
to use AOF and a StatefulSet volume claim:

```yaml
redis:
  persistence:
    enabled: true
    size: 1Gi
    storageClass: ""  # cluster default; "-" requests no StorageClass
```

Alternatively set `redis.persistence.existingClaim` to a claim in the release
namespace, with persistence enabled. The volume must support writes by UID/GID
999; the pod sets `fsGroup: 999`. New claims use `ReadWriteOnce`.

Choose storage when first installing. Kubernetes cannot change a StatefulSet's
volume-claim template in place: switching between ephemeral and persistent
storage or changing the claim template requires a planned migration/recreation.
StatefulSet-created PVCs remain after uninstall; delete them explicitly when
the stored data is no longer needed. Existing claims are never managed by Helm.

To connect to an existing Redis instead of deploying one:

```yaml
redis:
  enabled: false
externalRedis:
  address: redis.example.svc:6379
```

The current service supports a single plain Redis TCP endpoint without
authentication, TLS, Sentinel or Redis Cluster discovery. The bundled Redis
matches that interface and has no authentication. Redis persistence does not
provide replication or failover; bucket keys still expire after 60 seconds idle.

## Values

See [values.yaml](values.yaml) for all defaults.

| Value | Default | Purpose |
| --- | --- | --- |
| `replicaCount` | `2` | Stateless quota replicas sharing the backend |
| `image.repository`, `image.tag` | `bandwidth-quota`, `dev` | Quota image to run |
| `imagePullSecrets` | `[]` | Pull secrets used by both workloads |
| `service.type`, `service.port` | `ClusterIP`, `9300` | Quota service and listen port |
| `redis.enabled` | `true` | Deploy the bundled single Redis instance |
| `redis.image.repository`, `redis.image.tag` | `redis`, `7.4-alpine` | Redis image |
| `redis.port` | `6379` | Bundled Redis listen port |
| `redis.persistence.enabled` | `false` | Use a PVC and enable AOF |
| `externalRedis.address` | `""` | Required host:port when bundled Redis is disabled |
| `resources`, `redis.resources` | See values | CPU/memory requests and limits |
| `nodeSelector`, `tolerations`, `affinity` | Empty | Quota scheduling; Redis has its own equivalent values |
| `nameOverride`, `fullnameOverride` | `""` | Resource name overrides |

Both containers run as non-root with read-only root filesystems, dropped
capabilities and no service-account token. Ports must be at least 1024.
Quota probes use gRPC health; Redis probes check `PING`. The service's health
implementation reports process health, so quota readiness does not establish
Redis connectivity. Redis failures appear as errors on `AcquireLease` calls.

## Validate and remove

```bash
helm lint ./helm-chart --strict
helm template bandwidth ./helm-chart
helm template bandwidth ./helm-chart \
  --set redis.enabled=false --set externalRedis.address=redis.example.svc:6379
helm template bandwidth ./helm-chart --set redis.persistence.enabled=true

helm uninstall bandwidth --namespace bandwidth
```

For kind, use the same namespace, kubeconfig and context options as at installation.

Verified on September 30, 2026 with Helm 3.19.0 and 4.2.0 and the existing
Kubernetes 1.36.1 kind cluster:

- Strict lint and chart packaging passed. Default, external Redis, generated
  PVC, existing claim, custom port and no-storage-class manifests passed
  Kubernetes server validation. Invalid values were rejected by the schema.
- Bundled Redis, external Redis and persistent Redis installations became ready.
  Real gRPC calls through two distinct quota pods and a separate release using
  external Redis consumed the same bucket. Both RPC service names worked.
- Persistent Redis retained a test value across pod recreation, a chart-version
  upgrade, uninstall and reinstallation using the retained claim.

The temporary `bandwidth-helm-smoke` releases and namespace were removed after
verification. The existing static Envoy demo remains in `bandwidth-testing`.
