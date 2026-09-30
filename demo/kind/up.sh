#!/usr/bin/env bash
set -euo pipefail

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$DEMO_DIR/../.." && pwd)"
DEMO_KUBECONFIG="${DEMO_KUBECONFIG:-/private/tmp/bandwidth-testing.kubeconfig}"
DEMO_RATE_KIB="${DEMO_RATE_KIB:-1024}"
DEMO_REPLICAS="${DEMO_REPLICAS:-2}"
DEMO_ENVOY_IMAGE="${BANDWIDTH_ENVOY_IMAGE:-bandwidth-envoy:testing}"
export BUILDX_CONFIG="${BUILDX_CONFIG:-/private/tmp/bandwidth-buildx}"
export GOCACHE="${GOCACHE:-/private/tmp/bandwidth-go-build}"
export GOMODCACHE="${GOMODCACHE:-/private/tmp/bandwidth-go-mod}"

if ! docker image inspect "$DEMO_ENVOY_IMAGE" >/dev/null 2>&1; then
    printf 'Missing %s. Build the experimental Envoy first; see demo/kind/README.md.\n' "$DEMO_ENVOY_IMAGE" >&2
    exit 1
fi

if ! kind get clusters | rg -qx bandwidth-testing; then
    kind create cluster --name bandwidth-testing --image kindest/node:v1.36.1 \
        --kubeconfig "$DEMO_KUBECONFIG" --wait 60s
else
    kind get kubeconfig --name bandwidth-testing > "$DEMO_KUBECONFIG"
fi
chmod 600 "$DEMO_KUBECONFIG"

case "$(docker info --format '{{.Architecture}}')" in
    aarch64|arm64) DEMO_ARCH=arm64 ;;
    x86_64|amd64) DEMO_ARCH=amd64 ;;
    *) printf 'Unsupported Docker architecture\n' >&2; exit 1 ;;
esac
mkdir -p "$DEMO_DIR/.build"
cd "$REPO_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH="$DEMO_ARCH" go build -o "$DEMO_DIR/.build/bandwidth-quota" ./cmd/bandwidth-quota
docker build -q -t bandwidth-quota:testing -f "$DEMO_DIR/Dockerfile.quota" "$DEMO_DIR"
docker build -q -t bandwidth-traffic:testing -f "$DEMO_DIR/Dockerfile.traffic" "$DEMO_DIR"
kind load docker-image --name bandwidth-testing "$DEMO_ENVOY_IMAGE" bandwidth-quota:testing bandwidth-traffic:testing

python3 "$DEMO_DIR/generate.py" --rate-kib "$DEMO_RATE_KIB" --replicas "$DEMO_REPLICAS" \
    --envoy-image "$DEMO_ENVOY_IMAGE" > "$DEMO_DIR/.build/manifest.json"
DEMO_KUBECTL=(kubectl --kubeconfig "$DEMO_KUBECONFIG" --context kind-bandwidth-testing \
    --cache-dir /private/tmp/bandwidth-kube-cache -n bandwidth-testing)
"${DEMO_KUBECTL[@]}" apply -f "$DEMO_DIR/.build/manifest.json"
# Static configs and reused image tags require a restart when rerunning up.sh.
"${DEMO_KUBECTL[@]}" rollout restart deployment/bandwidth-quota deployment/traffic-upstream deployment/traffic-client statefulset/envoy
for resource in deployment/redis deployment/bandwidth-quota deployment/traffic-upstream deployment/traffic-client statefulset/envoy; do
    "${DEMO_KUBECTL[@]}" rollout status "$resource" --timeout=180s
done
"${DEMO_KUBECTL[@]}" get pods -o wide
printf '\nReady. Run: python3 %s/verify.py --kubeconfig %s --rate-kib %s --outage\n' "$DEMO_DIR" "$DEMO_KUBECONFIG" "$DEMO_RATE_KIB"
