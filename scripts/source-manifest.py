#!/usr/bin/env python3
"""Canonical reviewed-source identity, independent of Git tracking state.
Generated archives, reports, caches, VM metadata and secrets are never inputs.
"""
from __future__ import annotations
import argparse
import gzip
import hashlib
import io
import json
import pathlib
import re
import tarfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
FIXED = (
    'macos/VERSION', 'macos/App.swift', 'macos/Profile.swift', 'macos/Icon.swift',
    'macos/Info.plist', 'macos/build.sh', 'macos/README.zh-CN.md',
    'macos/VALIDATION.md', 'macos/tcp-profile.example.json',
    'macos/userspace-profile.example.json', 'macos/engine/go.mod',
    'scripts/build-userspace-landing.sh', 'scripts/package-userspace.py',
    'scripts/source-manifest.py', 'scripts/verify-userspace.py', 'scripts/release-gates.py', 'scripts/scheduler-gates.py',
)


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def collect(root: pathlib.Path = ROOT) -> dict[str, bytes]:
    names = set(FIXED)
    for directory, pattern in [('macos/engine', '*.go'), ('docs/userspace', '*.md'),
                               ('tests/userspace', '*.py'), ('tests/userspace', '*.swift'), ('tests/userspace', '*.go'), ('tests/userspace', '*.pl')]:
        names.update(p.relative_to(root).as_posix() for p in (root/directory).rglob(pattern))
    files = {}
    for name in sorted(names):
        path = root/name
        if path.is_symlink() or not path.is_file():
            raise ValueError('Missing or linked source: ' + name)
        data = path.read_bytes()
        data.decode('utf-8')
        if re.search(rb'/(?:Users|var/folders)/[A-Za-z0-9_.-]+/', data):
            raise ValueError('Private workstation path in source: ' + name)
        if re.search(rb'-----BEGIN (?:OPENSSH |RSA |EC )?PRIVATE KEY-----', data):
            raise ValueError('Private key in source: ' + name)
        files[name] = data
    return files


def manifest(files: dict[str, bytes]) -> bytes:
    return ''.join(f'{sha(data)}  {name}\n' for name, data in sorted(files.items())).encode()


def archive(path: pathlib.Path, files: dict[str, bytes]) -> None:
    """Stable gzip/USTAR bytes, neutral ownership, no host paths or timestamps."""
    with path.open('wb') as raw, gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode='w', format=tarfile.USTAR_FORMAT) as out:
            for name, data in sorted(files.items()):
                p = pathlib.PurePosixPath(name)
                if p.is_absolute() or '..' in p.parts:
                    raise ValueError('Unsafe archive name: '+name)
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                entry.mtime = entry.uid = entry.gid = 0
                entry.uname = entry.gname = 'root'
                entry.mode = 0o755 if name.endswith('.sh') or name == 'mptcp-landing' else 0o644
                out.addfile(entry, io.BytesIO(data))
    with tarfile.open(path, 'r:gz') as check:
        if set(check.getnames()) != set(files):
            raise ValueError('Archive inventory differs')
        for entry in check:
            if not entry.isfile() or entry.pax_headers:
                raise ValueError('Archive contains non-regular entries')
            stream = check.extractfile(entry)
            if stream is None or sha(stream.read()) != sha(files[entry.name]):
                raise ValueError('Archive content differs: '+entry.name)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('--id', action='store_true')
    parser.add_argument('--freeze', action='store_true')
    args = parser.parse_args()
    files = collect()
    sums = manifest(files)
    identity = sha(sums)
    if args.id:
        print(identity)
        return
    if not args.freeze:
        parser.error('select --id or --freeze')
    version = (ROOT/'macos/VERSION').read_text().strip()
    if not re.fullmatch(r'\d+\.\d+\.\d+', version):
        raise ValueError('Invalid release version')
    out = ROOT/'dist'/('userspace-'+version)
    out.mkdir(parents=True, exist_ok=True)
    name = f'MPTCP-Userspace-{version}-source.tar.gz'
    archive(out/name, dict(files, SOURCE_SHA256SUMS=sums, SOURCE_ID=(identity+'\n').encode()))
    if manifest(collect()) != sums:
        raise ValueError('Sources changed during freeze; archive is not a current release')
    (out/'SOURCE_SHA256SUMS').write_bytes(sums)
    (out/'SOURCE_ID').write_text(identity+'\n')
    print(json.dumps({'version':version, 'source_id':identity, 'source_files':len(files),
                      'archive':name, 'archive_sha256':sha((out/name).read_bytes()),
                      'scope':'Exact allowlisted bytes including untracked source, not a Git commit or authorship assertion'},indent=2))


if __name__ == '__main__':
    main()
