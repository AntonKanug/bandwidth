#!/usr/bin/env bash
set -euo pipefail

# Build the current Envoy working tree, including uncommitted and untracked files.
# ENVOY_CHECKOUT=/path/to/envoy RUN_ENVOY_TESTS=1 ./demo/kind/build-envoy.sh
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENVOY_CHECKOUT="${ENVOY_CHECKOUT:-/Users/antonk/envoy}"
RUN_ENVOY_TESTS="${RUN_ENVOY_TESTS:-0}"
ENVOY_IMAGE="${BANDWIDTH_ENVOY_IMAGE:-bandwidth-envoy:testing}"
BUILD_IMAGE=envoyproxy/envoy-build:devtools-v0.1.6
BUILD_VOLUME=udp-authz-envoy-build
BUILD_CONTAINER=bandwidth-envoy-repro-build
BUILD_CONTAINER_ID=

for tool in docker git python3; do
    command -v "$tool" >/dev/null
done
case "$RUN_ENVOY_TESTS" in
    0|1) ;;
    *) printf 'RUN_ENVOY_TESTS must be 0 or 1.\n' >&2; exit 1 ;;
esac
case "$(docker info --format '{{.Architecture}}')" in
    aarch64|arm64) ;;
    *) printf 'This local build script requires an ARM64 Docker engine.\n' >&2; exit 1 ;;
esac
docker image inspect "$BUILD_IMAGE" envoyproxy/envoy:v1.39.0 >/dev/null
docker volume inspect "$BUILD_VOLUME" >/dev/null
if docker container inspect "$BUILD_CONTAINER" >/dev/null 2>&1; then
    printf 'Container %s already exists; leaving it untouched.\n' "$BUILD_CONTAINER" >&2
    exit 1
fi

BUILD_DIR="$(mktemp -d /private/tmp/bandwidth-envoy-repro.XXXXXX)"
printf 'Saving the source snapshot and build logs in %s\n' "$BUILD_DIR"
python3 - "$ENVOY_CHECKOUT" "$BUILD_DIR" <<'PY'
import os
from pathlib import Path
import shutil
import subprocess
import sys

checkout = Path(sys.argv[1]).resolve()
output = Path(sys.argv[2])
source = output / "source"
source.mkdir()

def git(*args):
    return subprocess.check_output(["git", "-C", str(checkout), *args])

revision = git("rev-parse", "HEAD").decode().strip()
names = git("ls-files", "--cached", "--others", "--exclude-standard", "-z")
for name in set(names.split(b"\0")) - {b""}:
    relative = Path(os.fsdecode(name))
    original = checkout / relative
    if not original.exists() and not original.is_symlink():
        continue  # Tracked file deleted in the working tree.
    destination = source / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(original, destination, follow_symlinks=False)

(source / "SOURCE_VERSION").write_text(revision + "\n")
(output / "source-status.txt").write_bytes(git("status", "--short"))
(output / "source-revision.txt").write_text(revision + "\n")
if not (source / ".bazelversion").is_file():
    raise SystemExit("ENVOY_CHECKOUT must point to the root of the Envoy repository")
PY
cp -R "$DEMO_DIR/envoy-build-config" "$BUILD_DIR/envoy-build-config"
mkdir -p "$BUILD_DIR/output" "$BUILD_DIR/image"

cleanup() {
    if [[ -n "$BUILD_CONTAINER_ID" ]]; then
        docker rm --force "$BUILD_CONTAINER_ID" >/dev/null
    fi
}
trap cleanup EXIT
BUILD_CONTAINER_ID="$(docker run --detach --pull=never --platform=linux/arm64 \
    --name "$BUILD_CONTAINER" --user 0 --cpus=6 --memory=6g --memory-swap=6g \
    --mount "type=volume,source=$BUILD_VOLUME,target=/build" \
    --mount "type=bind,source=$BUILD_DIR,target=/work" \
    --entrypoint /bin/bash "$BUILD_IMAGE" -c 'exec sleep infinity')"

docker exec -i -e "RUN_ENVOY_TESTS=$RUN_ENVOY_TESTS" \
    -w /work/source "$BUILD_CONTAINER_ID" bash -s <<'BUILD' 2>&1 | tee "$BUILD_DIR/build.log"
set -euo pipefail

# Trust the existing local CA without disabling certificate verification.
if [[ -f /build/palantir-root-ca.pem ]]; then
    cp /build/palantir-root-ca.pem /usr/local/share/ca-certificates/bandwidth-build-root.crt
    update-ca-certificates
fi
jvm_options=(--host_jvm_args=-Xmx1024m)
if [[ -f /build/palantir-truststore.p12 ]]; then
    jvm_options+=(
        --host_jvm_args=-Djavax.net.ssl.trustStore=/build/palantir-truststore.p12
        --host_jvm_args=-Djavax.net.ssl.trustStorePassword=changeit
        --host_jvm_args=-Djavax.net.ssl.trustStoreType=PKCS12
    )
fi

bazel_version="$(tr -d '[:space:]' < .bazelversion)"
[[ "$bazel_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
bazel_filename="bazel-${bazel_version}-linux-arm64"
bazel_url="https://github.com/bazelbuild/bazel/releases/download/${bazel_version}/${bazel_filename}"
mkdir -p /work/tools
curl --fail --location --show-error --retry 3 --proto '=https' --proto-redir '=https' \
    "$bazel_url" -o "/work/tools/$bazel_filename"
curl --fail --location --show-error --retry 3 --proto '=https' --proto-redir '=https' \
    "${bazel_url}.sha256" -o "/work/tools/${bazel_filename}.sha256"
(cd /work/tools && sha256sum --check "${bazel_filename}.sha256")
chmod +x "/work/tools/$bazel_filename"

# Keep the source checkout, other containers, and their output bases untouched.
bazel=("/work/tools/$bazel_filename" --batch
    --output_base=/build/bandwidth-testing/repro-output-base "${jvm_options[@]}")
build_options=(
    --override_repository=+envoy_build_config_ext+envoy_build_config=/work/envoy-build-config
    --symlink_prefix=/work/output/bazel-
    --repository_cache=/build/.cache/bazel/_bazel_envoybuild/cache/repos
    --distdir=/build/distdir
    --jobs=6 --local_resources=memory=4600
    --define=google_grpc=disabled
    --color=no --curses=no --show_progress_rate_limit=15
)
if [[ -d /build/reverse-tunnel/v8-git ]]; then
    build_options+=(--override_repository=v8+=/build/reverse-tunnel/v8-git)
fi

"${bazel[@]}" build "${build_options[@]}" //source/exe:envoy-static
if [[ "$RUN_ENVOY_TESTS" == 1 ]]; then
    # Envoy's large mock translation units use more memory than production code.
    "${bazel[@]}" test "${build_options[@]}" --jobs=2 --local_resources=memory=3500 --test_output=errors \
        //test/extensions/common/distributed_token_bucket:distributed_token_bucket_test \
        //test/extensions/common/distributed_token_bucket:grpc_lease_fetcher_test \
        //test/extensions/filters/network/tcp_bandwidth_limit:filter_test \
        //test/extensions/filters/network/tcp_bandwidth_limit:config_test
fi
cp /work/output/bazel-bin/source/exe/envoy-static /work/image/envoy
chmod 755 /work/image/envoy
BUILD

cat > "$BUILD_DIR/image/Dockerfile" <<'DOCKERFILE'
FROM envoyproxy/envoy:v1.39.0
COPY envoy /usr/local/bin/envoy
ENTRYPOINT ["/usr/local/bin/envoy"]
CMD ["--help"]
DOCKERFILE
docker build --pull=false --network=none --platform=linux/arm64 \
    -t "$ENVOY_IMAGE" "$BUILD_DIR/image"
printf '\nBuilt %s. Source snapshot, binary, and logs: %s\n' "$ENVOY_IMAGE" "$BUILD_DIR"
