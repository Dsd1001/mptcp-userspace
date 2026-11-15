#!/usr/bin/env python3
"""Fail-closed packaging for the complete tunnel-only 1.2.x suite.

No historical acceptance, build-only provenance or skipped native job can
promote a release. This script does not upload, install or modify update feeds.
"""
from __future__ import annotations
import argparse
import hashlib
import importlib.util
import json
import pathlib
import re
import shutil
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('source_manifest', ROOT/'scripts/source-manifest.py')
source = importlib.util.module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(source)

REQUIRED_GATES = frozenset({
    'macos-unit', 'macos-race', 'macos-vet', 'macos-controller',
    'macos-ui', 'macos-process', 'macos-aggregation',
    'linux-unit', 'linux-race', 'linux-vet', 'linux-controller',
    'linux-process', 'linux-aggregation', 'linux-browser', 'linux-container',
    'windows-unit', 'windows-client', 'windows-installer', 'release-script-tests',
})
NATIVE_BINARIES = (
    'mptcp-client-linux-amd64', 'mptcp-client-linux-arm64',
    'mptcp-landing', 'mptcp-landing-linux-arm64',
    'mpx-provision', 'mpx-provision-linux-arm64',
)


def require(ok: bool, message: str) -> None:
    if not ok:
        raise ValueError(message)


def safe_file(root: pathlib.Path, name: str) -> pathlib.Path:
    rel = pathlib.PurePosixPath(name)
    require(bool(name) and not rel.is_absolute() and '..' not in rel.parts and '\\' not in name,
            'Unsafe evidence/asset path: '+name)
    path = root/name
    require(path.is_file() and not path.is_symlink() and path.resolve().is_relative_to(root.resolve()),
            'Missing, linked or out-of-tree evidence/asset: '+name)
    return path


def artifact_names(version: str) -> list[str]:
    require(re.fullmatch(r'1\.2\.\d+', version) is not None, 'Expected tunnel suite 1.2.x')
    names = [f'MPTCP-Desk-{version}-universal.dmg', 'MPTCP-Desk.BUILDINFO', 'appcast.xml',
             f'MPTCP-Desk-{version}-Windows-Setup.exe', f'MPTCP-Desk-{version}-Windows-Portable.zip',
             'MPTCP-Desk-Windows.exe', 'mptcp-engine.exe', 'MPTCP-Desk-Windows.BUILDINFO', 'windows-update.json',
             f'MPTCP-Userspace-{version}-source.tar.gz']
    for name in NATIVE_BINARIES:
        names.extend((name, name+'.BUILDINFO'))
    return names


def check_tests(tests: dict, root: pathlib.Path, version: str, identity: str) -> dict[str, bytes]:
    require(tests.get('version') == version and tests.get('source_id') == identity,
            'Acceptance evidence belongs to another version/source')
    require(tests.get('verified') is True, 'Acceptance has not passed')
    records = tests.get('commands', [])
    names = [r.get('name') for r in records]
    require(set(names) == REQUIRED_GATES and len(names) == len(REQUIRED_GATES),
            'Acceptance gate inventory missing, extra or duplicated')
    evidence = {}
    for r in records:
        require(r.get('exit_code') == 0 and r.get('source_id') == identity and r.get('status') == 'passed',
                'Failed, skipped or stale gate: '+str(r.get('name')))
        name = r.get('log', '')
        data = safe_file(root, name).read_bytes()
        require(hashlib.sha256(data).hexdigest() == r.get('sha256'), 'Acceptance log changed: '+name)
        evidence['evidence/'+name] = data
    native = tests.get('native_ci', {})
    for platform in ('linux', 'windows'):
        run = native.get(platform, {})
        require(run.get('conclusion') == 'success' and run.get('source_id') == identity
                and isinstance(run.get('run_id'), int) and run['run_id'] > 0
                and re.fullmatch(r'[0-9a-f]{40}', run.get('head_sha', '')) is not None,
                'Native CI not verified for current source: '+platform)
    require(native['linux']['head_sha'] == native['windows']['head_sha'],
            'Native platforms tested different commits')
    return evidence


def check_provenance(record: dict, out: pathlib.Path, version: str, identity: str) -> list[str]:
    require(record.get('version') == version and record.get('source_id') == identity
            and record.get('verified') is True and record.get('update_signatures_verified') is True,
            'Full independently verified provenance and update signatures are required')
    names = artifact_names(version)
    recorded = record.get('artifact_sha256', {})
    require(set(recorded) == set(names), 'Provenance does not cover the exact complete suite')
    for name in names:
        require(source.sha(safe_file(out, name).read_bytes()) == recorded[name], 'Asset changed after verification: '+name)
    for name in NATIVE_BINARIES:
        data = safe_file(out, name+'.sha256').read_text()
        require(data == source.sha((out/name).read_bytes())+'  '+name+'\n',
                'Checksum must contain only the portable filename: '+name)
    return names


def go_license(go: str) -> bytes:
    root = pathlib.Path(subprocess.check_output([go, 'env', 'GOROOT'], text=True).strip())
    for path in (root/'LICENSE', root.parent/'LICENSE'):
        if path.is_file() and not path.is_symlink():
            return path.read_bytes()
    raise ValueError('Go license notice missing; refusing incomplete distribution')


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check-only', action='store_true')
    args = parser.parse_args()
    version = (ROOT/'macos/VERSION').read_text().strip()
    require((ROOT/'provisioning/VERSION').read_text().strip() == version, 'Suite versions differ')
    identity = source.check_frozen(ROOT)
    out = ROOT/'dist'/('userspace-'+version)
    tests = json.loads(safe_file(out, 'TESTS.json').read_text())
    evidence = check_tests(tests, ROOT, version, identity)
    provenance = json.loads(safe_file(out, 'PROVENANCE.json').read_text())
    names = check_provenance(provenance, out, version, identity)
    if args.check_only:
        print(json.dumps({'version': version, 'source_id': identity, 'ready': True, 'assets': len(names)}))
        return
    release = {name: (out/name).read_bytes() for name in names}
    for name in (*[b+'.sha256' for b in NATIVE_BINARIES], 'SOURCE_ID', 'SOURCE_SHA256SUMS', 'TESTS.json', 'PROVENANCE.json'):
        release[name] = safe_file(out, name).read_bytes()
    release.update(evidence)
    for name in ('RELEASE-'+version+'.zh-CN.md', 'SHARED-TUNNEL-1.2.1.zh-CN.md', 'PROVISIONING.md', 'PROTOCOL.md'):
        release['docs/'+name] = safe_file(ROOT/'docs/userspace', name).read_bytes()
    release['examples/client.json'] = safe_file(ROOT, 'examples/shared-tunnel/client.example.json').read_bytes()
    release['examples/egress.json'] = safe_file(ROOT, 'examples/shared-tunnel/egress.example.json').read_bytes()
    go = shutil.which('go')
    require(go is not None, 'Go compiler location is required for license notice')
    release['GO-LICENSE.txt'] = go_license(go)
    acceptance = ('# '+version+' tunnel suite acceptance\n\nSource-ID: `'+identity+'`.\n\n'
                  'All required current-source native CI, local correctness and release-asset gates passed. '
                  'Per-gate logs, run IDs and exact asset hashes are attached.\n\n'
                  'TCP/UoT inlets share one MPX/4 Session and a common Carrier set. '
                  'Legacy multi-Profile transport is not supported.\n\n'
                  'Limitations: shaped loopback measurements are not WAN guarantees; macOS uses the '
                  'original stable-local certificate, not Apple notarization; x86_64 macOS checks use '
                  'Rosetta; publication is not a production installation.\n')
    release['ACCEPTANCE.md'] = acceptance.encode()
    release['SHA256SUMS'] = source.manifest(release)
    bundle = out/f'MPTCP-Userspace-{version}-release.tar.gz'
    source.archive(bundle, release)
    (out/'ACCEPTANCE.md').write_text(acceptance)
    public = {name: (out/name).read_bytes() for name in names + ['SOURCE_ID', 'SOURCE_SHA256SUMS', 'TESTS.json', 'PROVENANCE.json', 'ACCEPTANCE.md', bundle.name]}
    (out/f'MPTCP-Userspace-{version}-SHA256SUMS').write_bytes(source.manifest(public))
    require(source.check_frozen(ROOT) == identity, 'Sources changed during packaging')
    print(json.dumps({'version': version, 'source_id': identity, 'ready': True, 'archive': bundle.name,
                      'archive_sha256': source.sha(bundle.read_bytes()), 'publication': 'not performed'}, indent=2))


if __name__ == '__main__':
    main()
