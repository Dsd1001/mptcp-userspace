#!/usr/bin/env python3
"""Exercise compiled client/egress processes on loopback; never install services."""
from __future__ import annotations
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import socket
import subprocess
import tempfile
import threading
import time


class Network:
    def __init__(self) -> None:
        self.closed = threading.Event()
        self.lock = threading.Lock()
        self.sockets: set[socket.socket] = set()
        self.counts = [0] * 6

    def track(self, sock: socket.socket) -> None:
        with self.lock:
            self.sockets.add(sock)

    def discard(self, sock: socket.socket) -> None:
        with self.lock:
            self.sockets.discard(sock)
        sock.close()

    def listen(self, handler) -> int:
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0))
        listener.listen(128)
        listener.settimeout(0.2)
        self.track(listener)
        def accept() -> None:
            while not self.closed.is_set():
                try:
                    conn, _ = listener.accept()
                except socket.timeout:
                    continue
                except OSError:
                    return
                self.track(conn)
                def run(conn=conn) -> None:
                    try:
                        handler(conn)
                    except OSError:
                        pass
                    finally:
                        self.discard(conn)
                threading.Thread(target=run, daemon=True).start()
        threading.Thread(target=accept, daemon=True).start()
        return listener.getsockname()[1]

    def backend(self, label: bytes):
        def run(conn: socket.socket) -> None:
            conn.sendall(label)
            while True:
                data = conn.recv(65536)
                if not data:
                    conn.shutdown(socket.SHUT_WR)
                    return
                conn.sendall(data)
        return run

    def relay(self, index: int, target: int):
        def run(a: socket.socket) -> None:
            with self.lock:
                self.counts[index] += 1
            b = socket.create_connection(('127.0.0.1', target), timeout=5)
            b.settimeout(None)
            self.track(b)
            def copy(src: socket.socket, dst: socket.socket) -> None:
                try:
                    while True:
                        chunk = src.recv(65536)
                        if not chunk:
                            dst.shutdown(socket.SHUT_WR)
                            return
                        dst.sendall(chunk)
                except OSError:
                    for sock in (a, b):
                        try:
                            sock.shutdown(socket.SHUT_RDWR)
                        except OSError:
                            pass
            one = threading.Thread(target=copy, args=(a, b), daemon=True)
            two = threading.Thread(target=copy, args=(b, a), daemon=True)
            one.start(); two.start()
            one.join(); two.join()
            self.discard(b)
        return run

    def close(self) -> None:
        self.closed.set()
        with self.lock:
            sockets = list(self.sockets)
        for sock in sockets:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()


def reserve_port() -> socket.socket:
    sock = socket.socket()
    sock.bind(('127.0.0.1', 0))
    return sock


def wait_for(check, processes, timeout: float = 12) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for process in processes:
            if process.poll() is not None:
                raise RuntimeError(f'process exited early with {process.returncode}')
        if check():
            return
        time.sleep(0.03)
    raise TimeoutError('loopback smoke-test readiness timed out')


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--client', required=True, type=Path)
    parser.add_argument('--egress', required=True, type=Path)
    parser.add_argument('--report', required=True, type=Path)
    args = parser.parse_args()
    args.client = args.client.resolve(); args.egress = args.egress.resolve()
    args.report = args.report.resolve(); args.report.parent.mkdir(parents=True, exist_ok=True)
    network = Network()
    processes: list[subprocess.Popen] = []
    reserved: list[socket.socket] = []
    events: list[dict] = []
    event_lock = threading.Lock()
    result: dict = {}
    try:
        with tempfile.TemporaryDirectory(prefix='shared-smoke-', dir=args.report.parent) as tmp:
            directory = Path(tmp)
            key = secrets.token_hex(32)
            backend_ports = [network.listen(network.backend(f'target-{i}\n'.encode())) for i in range(1, 4)]
            egress_reserve = reserve_port()
            egress_port = egress_reserve.getsockname()[1]
            egress_reserve.close()
            config = {'schema_version': 1, 'listen_tcp': f'127.0.0.1:{egress_port}', 'transport_key': key, 'max_sessions': 2, 'scheduler_mode': 'aggregate', 'tcp_routes': [
                {'id': i + 1, 'backend': f'127.0.0.1:{port}'} for i, port in enumerate(backend_ports)]}
            private = directory / 'egress-private.json'
            private.write_text(json.dumps(config)); private.chmod(0o600)
            status = directory / 'status.json'
            with (args.report.parent / 'process-egress.log').open('w') as server_log, (args.report.parent / 'process-client.jsonl').open('w') as client_log:
                server = subprocess.Popen([str(args.egress), 'server', '--root', str(directory), '--config', str(private), '--status-file', str(status)], stdout=server_log, stderr=subprocess.STDOUT)
                processes.append(server)
                def server_ready() -> bool:
                    try:
                        with socket.create_connection(('127.0.0.1', egress_port), timeout=.1):
                            return True
                    except OSError:
                        return False
                wait_for(server_ready, processes)
                relay_ports = [network.listen(network.relay(i, egress_port)) for i in range(6)]
                reserved = [reserve_port() for _ in range(3)]
                ports = [sock.getsockname()[1] for sock in reserved]
                client_config = {'schema_version': 1, 'kind': 'shared_mpx4_tunnel', 'tunnel': {'transport_key': key, 'scheduler_mode': 'aggregate', 'relays': [{'host': '127.0.0.1', 'port': port} for port in relay_ports]}, 'inlets': [
                    {'id': name, 'listen_port': ports[i], 'route_id': i + 1} for i, name in enumerate(('hkbn', 'hkt', 'icable'))]}
                encoded = json.dumps(client_config).encode()
                validated = subprocess.run([str(args.client), 'validate-tunnel'], input=encoded, capture_output=True, check=True, timeout=5)
                for sock in reserved:
                    sock.close()
                reserved.clear()
                client = subprocess.Popen([str(args.client), 'run-tunnel'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                processes.append(client)
                assert client.stdin is not None and client.stdout is not None
                client.stdin.write(encoded); client.stdin.close()
                def collect() -> None:
                    assert client.stdout is not None
                    for raw in client.stdout:
                        line = raw.decode(errors='replace')
                        client_log.write(line); client_log.flush()
                        try:
                            item = json.loads(line)
                        except ValueError:
                            continue
                        with event_lock:
                            events.append(item)
                collector = threading.Thread(target=collect, daemon=True)
                collector.start()
                def ready() -> bool:
                    with event_lock:
                        return len({x.get('profile_id') for x in events if x.get('kind') == 'listening'}) == 3 and any(x.get('kind') == 'stats' and x.get('paths') == 6 for x in events)
                wait_for(ready, processes)
                def transfer(index: int) -> dict:
                    payload = os.urandom(1 << 20)
                    with socket.create_connection(('127.0.0.1', ports[index]), timeout=10) as conn:
                        conn.sendall(payload); conn.shutdown(socket.SHUT_WR)
                        chunks = []
                        while True:
                            chunk = conn.recv(65536)
                            if not chunk:
                                break
                            chunks.append(chunk)
                    received = b''.join(chunks)
                    expected = f'target-{index+1}\n'.encode() + payload
                    if received != expected:
                        raise RuntimeError(f'route {index+1} content/half-close verification failed')
                    return {'route_id': index + 1, 'verified_bytes': len(payload), 'sha256': hashlib.sha256(payload).hexdigest()}
                with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
                    transfers = list(executor.map(transfer, range(3)))
                wait_for(lambda: status.exists(), processes)
                server_status = json.loads(status.read_text())
                sessions = server_status['tcp']
                if len(sessions) != 1 or sessions[0]['paths'] != 6:
                    raise RuntimeError('egress did not observe one Session with six Carriers')
                with network.lock:
                    counts = list(network.counts)
                if counts != [1] * 6:
                    raise RuntimeError(f'expected six physical client connections, observed {counts}')
                result = {'scope': 'compiled processes on loopback, not WAN', 'platform': platform.platform(), 'source_id': server_status['source_id'], 'sessions': 1, 'carriers': 6, 'relay_accept_counts': counts, 'transfers': transfers, 'client_sha256': hashlib.sha256(args.client.read_bytes()).hexdigest(), 'egress_sha256': hashlib.sha256(args.egress.read_bytes()).hexdigest(), 'validate_exit': validated.returncode}
                client.terminate(); client.wait(timeout=5)
                collector.join(timeout=5)
                server.terminate(); server.wait(timeout=5)
                result['shutdown_exit_codes'] = [client.returncode, server.returncode]
                if result['shutdown_exit_codes'] != [0, 0]:
                    raise RuntimeError(f'unclean shutdown: {result["shutdown_exit_codes"]}')
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill(); process.wait(timeout=5)
        for sock in reserved:
            sock.close()
        network.close()
    result['passed'] = True
    args.report.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
