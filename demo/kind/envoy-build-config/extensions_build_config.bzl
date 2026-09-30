# Extensions needed by the raw TCP demo and the quota service's gRPC client.
EXTENSIONS = {
    "envoy.access_loggers.file": "//source/extensions/access_loggers/file:config",
    "envoy.access_loggers.stdout": "//source/extensions/access_loggers/stream:config",
    "envoy.clusters.static": "//source/extensions/clusters/static:static_cluster_lib",
    "envoy.clusters.dns": "//source/extensions/clusters/dns:dns_cluster_lib",
    "envoy.clusters.strict_dns": "//source/extensions/clusters/strict_dns:strict_dns_cluster_lib",
    "envoy.filters.http.router": "//source/extensions/filters/http/router:config",
    "envoy.filters.network.http_connection_manager": "//source/extensions/filters/network/http_connection_manager:config",
    "envoy.filters.network.tcp_bandwidth_limit": "//source/extensions/filters/network/tcp_bandwidth_limit:config",
    "envoy.filters.network.tcp_proxy": "//source/extensions/filters/network/tcp_proxy:config",
    "envoy.load_balancing_policies.cluster_provided": "//source/extensions/load_balancing_policies/cluster_provided:config",
    "envoy.load_balancing_policies.round_robin": "//source/extensions/load_balancing_policies/round_robin:config",
    "envoy.network.dns_resolver.cares": "//source/extensions/network/dns_resolver/cares:config",
    "envoy.transport_sockets.raw_buffer": "//source/extensions/transport_sockets/raw_buffer:config",
    "envoy.upstreams.http.http": "//source/extensions/upstreams/http/http:config",
    "envoy.upstreams.http.tcp": "//source/extensions/upstreams/http/tcp:config",
}

EXTENSION_CONFIG_VISIBILITY = ["//:extension_config", "//:contrib_library", "//:mobile_library"]
EXTENSION_PACKAGE_VISIBILITY = ["//:extension_library", "//:contrib_library", "//:mobile_library"]
CONTRIB_EXTENSION_PACKAGE_VISIBILITY = ["//:contrib_library"]
MOBILE_PACKAGE_VISIBILITY = ["//:mobile_library"]
LEGACY_ALWAYSLINK = 1
