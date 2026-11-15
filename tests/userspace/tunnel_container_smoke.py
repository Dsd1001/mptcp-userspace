#!/usr/bin/env python3
"""Disposable nonroot controller container acceptance; no host service changes."""
from __future__ import annotations
import argparse
import base64
import json
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def run(*args: str) -> str:
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--report', required=True, type=Path)
    args = parser.parse_args()
    name = 'mpx-tunnel-test-' + secrets.token_hex(6)
    volume = name + '-data'
    password = secrets.token_hex(24)
    user = 'admin'
    container = ''
    result = {}
    args.report.parent.mkdir(parents=True, exist_ok=True)
    try:
        run('docker', 'volume', 'create', volume)
        container = run('docker', 'run', '-d', '--name', name, '-p', '127.0.0.1::8088', '-v', volume + ':/data', '-e', 'MPX_PROVISION_ADMIN_USER=' + user, '-e', 'MPX_PROVISION_ADMIN_PASSWORD=' + password, args.image)
        mapped = run('docker', 'port', name, '8088/tcp').splitlines()[0]
        base = 'http://' + mapped
        authorization = 'Basic ' + base64.b64encode((user + ':' + password).encode()).decode()
        def request(path: str, method: str = 'GET', payload=None):
            data = None if payload is None else json.dumps(payload).encode()
            req = urllib.request.Request(base + path, data=data, method=method, headers={'Authorization': authorization, 'Content-Type': 'application/json'})
            with urllib.request.urlopen(req, timeout=5) as response:
                raw = response.read()
                return response.status, raw
        def ready():
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                try:
                    code, _ = request('/healthz')
                    if code == 200:
                        return
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(.1)
            raise RuntimeError('container readiness failed')
        ready()
        configured_user = run('docker', 'inspect', '--format', '{{.Config.User}}', name)
        if configured_user in ('', '0', 'root', '0:0'):
            raise RuntimeError('controller container is configured to run as root')
        key = secrets.token_hex(32)
        config = {'schema_version': 1, 'kind': 'shared_mpx4_tunnel', 'tunnel': {'transport_key': key, 'relays': [{'host': '192.0.2.1', 'port': 24001}, {'host': '192.0.2.2', 'port': 24001}]}, 'inlets': [{'id': 'test', 'listen_port': 1081, 'route_id': 1}]}
        code, raw = request('/admin/api/tunnels', 'POST', {'name': 'Container acceptance', 'config': config})
        if code != 201:
            raise RuntimeError('controller cannot persist a tunnel as nonroot')
        record = json.loads(raw)
        with tempfile.TemporaryDirectory(prefix='mpx-container-') as temporary:
            copied = Path(temporary) / 'tunnels.json'
            run('docker', 'cp', name + ':/data/tunnels.json', str(copied))
            doc = json.loads(copied.read_text())
            if len(doc['tunnels']) != 1 or doc['tunnels'][0]['id'] != record['id']:
                raise RuntimeError('persisted record mismatch')
            if copied.stat().st_mode & 0o077:
                raise RuntimeError('container secret store is not private')
        first_mapping = mapped
        run('docker', 'restart', name)
        mapped = run('docker', 'port', name, '8088/tcp').splitlines()[0]
        base = 'http://' + mapped
        ready()
        _, raw = request('/admin/api/tunnels')
        rows = json.loads(raw)
        if len(rows) != 1 or rows[0]['id'] != record['id'] or rows[0]['revision'] != 1:
            raise RuntimeError('tunnel store did not survive restart')
        _, raw = request('/admin/api/system')
        identity = json.loads(raw)
        if identity.get('version') != (Path(__file__).resolve().parents[2] / 'provisioning/VERSION').read_text().strip() or identity.get('api_schema') != 4:
            raise RuntimeError('container suite identity mismatch')
        result = {'passed': True, 'scope': 'native Linux Docker, disposable named volume, create/restart/reload/private-file checks', 'configured_user': configured_user, 'initial_port': first_mapping, 'restarted_port': mapped, 'version': identity['version'], 'source_id': identity['source_id'], 'api_schema': identity['api_schema']}
        args.report.write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result, indent=2))
    except Exception:
        if container:
            log = run('docker', 'logs', name)
            state = run('docker', 'inspect', '--format', '{{json .State}}', name)
            ports = run('docker', 'port', name)
            args.report.with_suffix('.log').write_text(log + '\nSTATE=' + state + '\nPORTS=' + ports)
        raise
    finally:
        if container:
            subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        subprocess.run(['docker', 'volume', 'rm', volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    main()
