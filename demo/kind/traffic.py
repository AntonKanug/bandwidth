#!/usr/bin/env python3
"""Raw TCP traffic with receiver-confirmed byte counts for the kind demo."""

import argparse
import json
import socket
import socketserver
import threading
import time

CHUNK = b"x" * (64 * 1024)


def receive(sock, length):
    remaining = length
    while remaining:
        data = sock.recv(min(remaining, 65536))
        if not data:
            raise EOFError("connection closed")
        remaining -= len(data)


class TrafficHandler(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            direction = self.request.recv(1)
            if direction == b"D":
                while True:
                    self.request.sendall(CHUNK)
            elif direction == b"U":
                while True:
                    receive(self.request, len(CHUNK))
                    # Count acknowledged upstream delivery, not bytes queued in
                    # the client's or Envoy's socket buffers.
                    self.request.sendall(b"+")
        except (OSError, EOFError):
            pass


class TrafficServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def measure(args):
    stopped = threading.Event()
    lock = threading.Lock()
    counts = {target: 0 for target in args.targets}
    errors = []
    sockets = []

    def transfer(target):
        try:
            host, port = target.rsplit(":", 1)
            sock = socket.create_connection((host, int(port)), timeout=5)
            with lock:
                sockets.append(sock)
            with sock:
                sock.settimeout(args.seconds + args.warmup + 10)
                sock.sendall(b"D" if args.direction == "download" else b"U")
                while not stopped.is_set():
                    if args.direction == "download":
                        data = sock.recv(65536)
                        if not data:
                            raise EOFError("connection closed")
                        delivered = len(data)
                    else:
                        sock.sendall(CHUNK)
                        receive(sock, 1)
                        delivered = len(CHUNK)
                    with lock:
                        counts[target] += delivered
        except (OSError, EOFError) as exc:
            if not stopped.is_set():
                with lock:
                    errors.append({"target": target, "error": str(exc)})

    threads = [threading.Thread(target=transfer, args=(target,), daemon=True)
               for target in args.targets]
    for thread in threads:
        thread.start()
    time.sleep(args.warmup)
    with lock:
        baseline = counts.copy()
    started = time.monotonic()
    samples = []
    while True:
        remaining = args.seconds - (time.monotonic() - started)
        if remaining <= 0:
            break
        time.sleep(min(1, remaining))
        with lock:
            samples.append({"elapsed_s": round(time.monotonic() - started, 3),
                            "bytes": {key: counts[key] - baseline[key] for key in counts}})
    elapsed = time.monotonic() - started
    with lock:
        delivered = {key: counts[key] - baseline[key] for key in counts}
    stopped.set()
    for sock in sockets:
        try:
            sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
    for thread in threads:
        thread.join(timeout=2)
    total_kib = sum(delivered.values()) / elapsed / 1024
    result = {
        "direction": args.direction,
        "seconds": round(elapsed, 3),
        "warmup_s": args.warmup,
        "targets": {key: {"delivered_bytes": delivered[key],
                           "kib_per_second": round(delivered[key] / elapsed / 1024, 2)}
                    for key in delivered},
        "aggregate_kib_per_second": round(total_kib, 2),
        "errors": errors,
        "samples": samples,
    }
    passed = not errors
    if args.expected_kib is not None:
        lower = args.expected_kib * (1 - args.tolerance)
        upper = args.expected_kib * (1 + args.tolerance)
        passed = passed and lower <= total_kib <= upper
        result.update(expected_kib_per_second=args.expected_kib,
                      allowed_range_kib_per_second=[lower, upper])
    if args.require_each:
        passed = passed and all(value > 0 for value in delivered.values())
    result["passed"] = passed
    print(json.dumps(result, indent=2))
    return 0 if passed else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    server = sub.add_parser("server")
    server.add_argument("--port", type=int, default=8000)
    sub.add_parser("idle")
    client = sub.add_parser("measure")
    client.add_argument("--targets", nargs="+", required=True)
    client.add_argument("--direction", choices=["upload", "download"], default="download")
    client.add_argument("--seconds", type=float, default=20)
    client.add_argument("--warmup", type=float, default=3)
    client.add_argument("--expected-kib", type=float)
    client.add_argument("--tolerance", type=float, default=0.25)
    client.add_argument("--require-each", action="store_true")
    args = parser.parse_args()
    if args.command == "server":
        with TrafficServer(("0.0.0.0", args.port), TrafficHandler) as listener:
            print(f"TCP traffic server listening on :{args.port}", flush=True)
            listener.serve_forever()
    elif args.command == "idle":
        threading.Event().wait()
    else:
        if args.seconds <= 0 or args.warmup < 0 or len(set(args.targets)) != len(args.targets):
            parser.error("positive duration, nonnegative warmup and distinct targets required")
        return measure(args)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
