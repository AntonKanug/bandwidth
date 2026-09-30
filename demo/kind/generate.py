#!/usr/bin/env python3
"""Render a static TCP bandwidth demo for the bandwidth-testing kind cluster.

The configured KiB/s rate applies separately to upload (Envoy read) and
download (Envoy write), shared across all Envoy instances. This demo does not
run ECP or install production CRDs.
"""

import argparse
import json
import os
import sys


def positive_integer(value):
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def socket_address(address, port):
    return {"socket_address": {"address": address, "port_value": port}}


def cluster(name, port, grpc=False):
    result = {
        "name": name,
        "type": "STRICT_DNS",
        "connect_timeout": "1s",
        "lb_policy": "ROUND_ROBIN",
        "dns_lookup_family": "V4_ONLY",
        "load_assignment": {
            "cluster_name": name,
            "endpoints": [{"lb_endpoints": [{
                "endpoint": {"address": socket_address(name, port)}
            }]}],
        },
    }
    if grpc:
        result["circuit_breakers"] = {"thresholds": [{
            "max_connections": 16,
            "max_pending_requests": 64,
            "max_requests": 256,
            "max_retries": 3,
        }]}
        result["typed_extension_protocol_options"] = {
            "envoy.extensions.upstreams.http.v3.HttpProtocolOptions": {
                "@type": "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
                "explicit_http_config": {"http2_protocol_options": {}},
            }
        }
    return result


def envoy_config(rate_kib, namespace):
    key = f"kind/bandwidth-testing/{namespace}/demo-policy"
    limiter = {
        "name": "envoy.filters.network.tcp_bandwidth_limit",
        "typed_config": {
            "@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_bandwidth_limit.v3.TcpBandwidthLimit",
            "stat_prefix": "demo_policy",
            "read_limit_kbps": rate_kib,
            "write_limit_kbps": rate_kib,
            "fill_interval": "0.05s",
            "distributed": {
                "quota_service": {"envoy_grpc": {"cluster_name": "bandwidth-quota"}},
                "key": key,
                "lease_kb": 64,
                "fail_open": False,
            },
        },
    }
    tcp_proxy = {
        "name": "envoy.filters.network.tcp_proxy",
        "typed_config": {
            "@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
            "stat_prefix": "tcp",
            "cluster": "traffic-upstream",
        },
    }
    return {
        "admin": {"address": socket_address("0.0.0.0", 9901)},
        "static_resources": {
            "listeners": [{
                "name": "route_demo_policy",
                "address": socket_address("0.0.0.0", 9000),
                "filter_chains": [{"filters": [limiter, tcp_proxy]}],
            }],
            "clusters": [cluster("bandwidth-quota", 9300, grpc=True), cluster("traffic-upstream", 8000)],
        },
    }


def resource(kind, name, spec=None, api_version="v1", **fields):
    result = {
        "apiVersion": api_version,
        "kind": kind,
        "metadata": {"name": name},
        **fields,
    }
    if spec is not None:
        result["spec"] = spec
    return result


def container(name, image, args, ports=()):
    result = {
        "name": name,
        "image": image,
        "imagePullPolicy": "IfNotPresent",
        "args": args,
        "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}},
    }
    if ports:
        result["ports"] = [{"name": port_name, "containerPort": port} for port_name, port in ports]
        result["readinessProbe"] = {
            "tcpSocket": {"port": ports[0][1]},
            "initialDelaySeconds": 1,
            "periodSeconds": 2,
        }
    return result


def deployment(name, image, args, ports=(), replicas=1):
    return resource("Deployment", name, {
        "replicas": replicas,
        "selector": {"matchLabels": {"app": name}},
        "template": {
            "metadata": {"labels": {"app": name}},
            "spec": {"containers": [container(name, image, args, ports)]},
        },
    }, api_version="apps/v1")


def service(name, ports, headless=False):
    spec = {
        "selector": {"app": name},
        "ports": [{"name": port_name, "port": port, "targetPort": port} for port_name, port in ports],
    }
    if headless:
        spec["clusterIP"] = "None"
    return resource("Service", name, spec)


def render(args):
    envoy = container("envoy", args.envoy_image, [
        "-c", "/etc/envoy/envoy.json", "--concurrency", "1", "--log-level", "info",
    ], [("tcp", 9000), ("admin", 9901)])
    envoy["readinessProbe"] = {"httpGet": {"path": "/ready", "port": 9901}, "periodSeconds": 2}
    envoy["volumeMounts"] = [{"name": "config", "mountPath": "/etc/envoy", "readOnly": True}]
    manifest = {
        "apiVersion": "v1",
        "kind": "List",
        "items": [
            {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": args.namespace}},
            resource("ConfigMap", "envoy-config", data={"envoy.json": json.dumps(envoy_config(args.rate_kib, args.namespace), indent=2) + "\n"}),
            deployment("redis", args.redis_image, ["--save", "", "--appendonly", "no"], [("redis", 6379)]),
            service("redis", [("redis", 6379)]),
            deployment("bandwidth-quota", args.quota_image, ["--listen=:9300", "--redis-addr=redis:6379"], [("grpc", 9300)], replicas=2),
            service("bandwidth-quota", [("grpc", 9300)]),
            resource("StatefulSet", "envoy", {
                "serviceName": "envoy",
                "replicas": args.replicas,
                "selector": {"matchLabels": {"app": "envoy"}},
                "template": {
                    "metadata": {"labels": {"app": "envoy"}},
                    "spec": {
                        "containers": [envoy],
                        "volumes": [{"name": "config", "configMap": {"name": "envoy-config"}}],
                    },
                },
            }, api_version="apps/v1"),
            service("envoy", [("tcp", 9000), ("admin", 9901)], headless=True),
            deployment("traffic-upstream", args.traffic_image, ["server", "--port", "8000"], [("tcp", 8000)]),
            service("traffic-upstream", [("tcp", 8000)]),
            deployment("traffic-client", args.traffic_image, ["idle"]),
        ],
    }
    for item in manifest["items"]:
        if item["kind"] != "Namespace":
            item["metadata"]["namespace"] = args.namespace
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rate-kib", type=positive_integer, default=1024, help="global KiB/s budget in each direction")
    parser.add_argument("--replicas", type=positive_integer, default=2, help="number of Envoy instances sharing the route budget")
    parser.add_argument("--namespace", default="bandwidth-testing", help="isolated demo Kubernetes namespace")
    parser.add_argument("--envoy-image", default=os.environ.get("BANDWIDTH_ENVOY_IMAGE", "bandwidth-envoy:testing"))
    parser.add_argument("--quota-image", default=os.environ.get("BANDWIDTH_QUOTA_IMAGE", "bandwidth-quota:testing"))
    parser.add_argument("--traffic-image", default=os.environ.get("BANDWIDTH_TRAFFIC_IMAGE", "bandwidth-traffic:testing"))
    parser.add_argument("--redis-image", default=os.environ.get("BANDWIDTH_REDIS_IMAGE", "redis:7-alpine"))
    args = parser.parse_args()
    json.dump(render(args), sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
