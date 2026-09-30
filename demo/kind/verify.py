#!/usr/bin/env python3
"""Measure the real TCP demo in the explicitly selected kind cluster."""

import argparse
import datetime
import json
from pathlib import Path
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", default="/private/tmp/bandwidth-testing.kubeconfig")
    parser.add_argument("--namespace", default="bandwidth-testing")
    parser.add_argument("--rate-kib", type=int, default=1024)
    parser.add_argument("--seconds", type=int, default=20)
    parser.add_argument("--outage", action="store_true", help="also temporarily stop the demo quota deployment")
    parser.add_argument("--output", type=Path, default=Path(__file__).parent / "results")
    args = parser.parse_args()
    kube = ["kubectl", "--kubeconfig", args.kubeconfig, "--context", "kind-bandwidth-testing", "-n", args.namespace]
    args.output.mkdir(parents=True, exist_ok=True)
    results = {}

    def run(arguments, **kwargs):
        return subprocess.run(kube + arguments, check=True, text=True, capture_output=True, **kwargs).stdout

    def measure(name, targets, direction="download", expected=None, seconds=None):
        print(f"Measuring {name}...", flush=True)
        command = ["exec", "deployment/traffic-client", "--", "python3", "/app/traffic.py",
                   "measure", "--direction", direction, "--seconds", str(seconds or args.seconds),
                   "--warmup", "3", "--expected-kib", str(args.rate_kib if expected is None else expected),
                   "--targets", *targets]
        if expected != 0:
            command.append("--require-each")
        process = subprocess.run(kube + command, text=True, capture_output=True)
        if process.stdout.strip():
            result = json.loads(process.stdout)
            results[name] = result
            (args.output / f"{name}.json").write_text(json.dumps(result, indent=2) + "\n")
            print(f"  {result['aggregate_kib_per_second']:.2f} KiB/s aggregate, passed={result['passed']}", flush=True)
        if process.returncode:
            raise RuntimeError(f"{name} failed: {process.stderr or process.stdout}")

    targets = [f"envoy-{i}.envoy:9000" for i in range(3)]
    initial_state = json.loads(run(["get", "statefulset/envoy", "-o", "json"]))
    initial_replicas = initial_state["spec"]["replicas"]
    quota_replicas = json.loads(run(["get", "deployment/bandwidth-quota", "-o", "json"]))["spec"]["replicas"]
    try:
        run(["scale", "statefulset/envoy", "--replicas=2"])
        run(["rollout", "status", "statefulset/envoy", "--timeout=120s"])
        measure("one-envoy-download", targets[:1])
        measure("two-envoys-download", targets[:2])
        measure("two-envoys-upload", targets[:2], direction="upload")
        run(["scale", "statefulset/envoy", "--replicas=3"])
        run(["rollout", "status", "statefulset/envoy", "--timeout=120s"])
        measure("three-envoys-download", targets)
        measure("one-active-of-three", targets[1:2])
        if args.outage:
            run(["scale", "deployment/bandwidth-quota", "--replicas=0"])
            run(["wait", "--for=delete", "pod", "-l", "app=bandwidth-quota", "--timeout=60s"])
            measure("quota-outage-fail-closed", targets[:2], expected=0, seconds=6)
            run(["scale", "deployment/bandwidth-quota", f"--replicas={quota_replicas}"])
            run(["rollout", "status", "deployment/bandwidth-quota", "--timeout=120s"])
            measure("quota-recovery", targets[:2])
        # Read the distributed counters; TCP rate gauges in this prototype are
        # last-writer-per-connection values and are not the throughput oracle.
        stats_code = "import urllib.request; print(urllib.request.urlopen('http://%s:9901/stats?filter=distributed_bandwidth_limit').read().decode())"
        for i in range(3):
            stats = run(["exec", "deployment/traffic-client", "--", "python3", "-c", stats_code % f"envoy-{i}.envoy"])
            (args.output / f"envoy-{i}-stats.txt").write_text(stats)
    finally:
        run(["scale", "deployment/bandwidth-quota", f"--replicas={quota_replicas}"])
        run(["scale", "statefulset/envoy", f"--replicas={initial_replicas}"])
        run(["rollout", "status", "deployment/bandwidth-quota", "--timeout=120s"])
        run(["rollout", "status", "statefulset/envoy", "--timeout=120s"])
    report = {
        "timestamp_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "context": "kind-bandwidth-testing",
        "rate_kib_per_second_per_direction": args.rate_kib,
        "envoy_version": run(["exec", "envoy-0", "--", "/usr/local/bin/envoy", "--version"]).strip(),
        "results": results,
        "pods": json.loads(run(["get", "pods", "-o", "json"])),
    }
    (args.output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(f"All checks passed. Results: {args.output.resolve()}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (subprocess.CalledProcessError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError):
            print(error.stderr, file=sys.stderr)
        raise SystemExit(1)
