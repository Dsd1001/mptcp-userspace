#!/usr/bin/env python3
"""Publish an already accepted complete tunnel suite through an authorized gh CLI.

Uses gh's normal credential store and Sparkle's normal signing interface. Does
not export credentials, disable verification, or touch installed services.
A draft is uploaded and read back before the release is made public.
"""
from __future__ import annotations
import argparse
import hashlib
import importlib.util
import json
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
REPO = 'Dsd1001/mptcp-userspace'
spec = importlib.util.spec_from_file_location('tunnel_suite', ROOT/'scripts/tunnel-suite.py')
suite = importlib.util.module_from_spec(spec)
spec.loader.exec_module(suite)


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, capture_output=True, timeout=kwargs.pop('timeout', 180), **kwargs).stdout


def api(path: str):
    return json.loads(run(['gh', 'api', 'repos/'+REPO+'/'+path]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publish', action='store_true', help='Upload/read back a draft and then make it public')
    args = parser.parse_args()
    version = (ROOT/'macos/VERSION').read_text().strip()
    identity = suite.source.check_frozen(ROOT)
    out = ROOT/'dist'/('userspace-'+version)
    tests = json.loads((out/'TESTS.json').read_text())
    suite.check_tests(tests, ROOT, version, identity)
    head = run(['git', 'rev-parse', 'HEAD']).strip()
    for platform in ('linux', 'windows'):
        receipt = tests['native_ci'][platform]
        live = api('actions/runs/'+str(receipt['run_id']))
        if live.get('conclusion') != 'success' or live.get('head_sha') != receipt['head_sha']:
            raise ValueError('Live CI disagrees with '+platform+' acceptance')
        if receipt['head_sha'] != head:
            raise ValueError('Local HEAD is not the accepted commit')
    # Signing failure leaves the version unpublished. Never create a new key.
    for script in ('build-appcast.sh', 'build-windows-update.sh'):
        arguments=['bash', str(ROOT/'scripts'/script)]
        if script=='build-windows-update.sh':
            arguments += [str(out/f'MPTCP-Desk-{version}-Windows-Setup.exe'), str(out/'windows-update.json')]
        print(run(arguments, timeout=120).strip())
    print(run(['python3', 'scripts/verify-userspace.py'], timeout=900).strip())
    print(run(['python3', 'scripts/package-userspace.py'], timeout=180).strip())
    if not args.publish:
        print('Verified complete suite; no remote publication requested.')
        return
    sums=out/f'MPTCP-Userspace-{version}-SHA256SUMS'
    expected={}
    for line in sums.read_text().splitlines():
        digest,name=line.split('  ',1)
        suite.safe_file(out,name)
        if hashlib.sha256((out/name).read_bytes()).hexdigest()!=digest:
            raise ValueError('Final release checksum changed: '+name)
        expected[name]=digest
    expected[sums.name]=hashlib.sha256(sums.read_bytes()).hexdigest()
    tag='v'+version
    probe=subprocess.run(['gh','release','view',tag,'--repo',REPO,'--json','isDraft,targetCommitish'],cwd=ROOT,capture_output=True,text=True,timeout=30)
    if probe.returncode==0:
        existing=json.loads(probe.stdout)
        if not existing['isDraft'] or existing['targetCommitish']!=head:
            raise ValueError('Refusing to overwrite an existing published or different release')
    else:
        run(['gh','release','create',tag,'--repo',REPO,'--draft','--target',head,'--title','MPTCP Tunnel '+version,
             '--notes-file',str(ROOT/'docs/userspace'/('RELEASE-'+version+'.zh-CN.md'))])
    run(['gh','release','upload',tag,'--repo',REPO,'--clobber',*[str(out/n) for n in expected]],timeout=600)
    with tempfile.TemporaryDirectory(prefix='mpx-release-readback-') as temporary:
        run(['gh','release','download',tag,'--repo',REPO,'--dir',temporary],timeout=600)
        actual={p.name for p in pathlib.Path(temporary).iterdir() if p.is_file()}
        if actual!=set(expected): raise ValueError('Remote release inventory differs; draft retained')
        for name,digest in expected.items():
            if hashlib.sha256((pathlib.Path(temporary)/name).read_bytes()).hexdigest()!=digest:
                raise ValueError('Remote asset mismatch; draft retained: '+name)
    run(['gh','release','edit',tag,'--repo',REPO,'--draft=false','--latest'])
    live=json.loads(run(['gh','release','view',tag,'--repo',REPO,'--json','isDraft,url,tagName']))
    if live['isDraft'] or live['tagName']!=tag: raise ValueError('Publication confirmation failed')
    receipt={'version':version,'source_id':identity,'commit':head,'published':True,'url':live['url'],'asset_sha256':expected}
    (out/'PUBLICATION.json').write_text(json.dumps(receipt,indent=2)+'\n')
    print(json.dumps(receipt,indent=2))


if __name__=='__main__': main()
