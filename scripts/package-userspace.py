#!/usr/bin/env python3
"""Package frozen 0.9.4 / MPX/3 Rev5 artifacts, including the formal Weighted feature release."""
from __future__ import annotations
import argparse, importlib.util, json, os, pathlib, shutil, subprocess, tarfile
ROOT=pathlib.Path(__file__).resolve().parents[1]


def module(name: str, path: pathlib.Path):
    spec=importlib.util.spec_from_file_location(name,path)
    loaded=importlib.util.module_from_spec(spec);spec.loader.exec_module(loaded)
    return loaded
source=module('source_manifest',ROOT/'scripts/source-manifest.py')
gates=module('release_gates',ROOT/'scripts/release-gates.py')
scheduler_gates=module('scheduler_gates',ROOT/'scripts/scheduler-gates.py')


def local_test_secrets() -> list[bytes]:
    secrets=[]
    for name in ['live-071','live-080']:
        token=ROOT/'macos/build'/name/'fixture-token'
        if token.is_file() and len(token.read_bytes().strip())>=32:secrets.append(token.read_bytes().strip())
    meta=ROOT/'macos/build/debian-vm.json'
    if meta.is_file():
        directory=pathlib.Path(json.loads(meta.read_text())['directory'])
        for name in ['ss-private.json','landing-private.json']:
            path=directory/name
            if path.is_file():
                data=json.loads(path.read_text())
                for key in ['password','transport_key']:
                    value=data.get(key)
                    if isinstance(value,str) and len(value)>=16:secrets.append(value.encode())
    return secrets


def acceptance_text(identity: str, capacity: dict, runtime: dict, complete: bool, scheduler: dict, *, version: str, untested: bool=False, preview: bool=False, weighted_release: bool=False) -> str:
    text=f'# {version} / MPX/3 Rev5 acceptance\n\n'
    stage='weighted-feature-release' if weighted_release else ('preview-with-known-limitations' if preview else ('untested-by-request release' if untested else ('short-capacity-and-physical-validated' if complete else 'candidate; required acceptance pending')))
    text+=f'Source-ID: `{identity}`. Stage: **{stage}**.\n\n'
    if weighted_release:
        text+='Formal Weighted feature release. Current-source correctness, authenticated directional capacity configuration, failure protection and source-matched Weighted laboratory throughput are recorded in TESTS.json and SCHEDULER-MODES.json. Full 30-second capacity matrices and physical App+Surge/WAN acceptance are not claimed unless separately verified.\n\n'
    if preview:
        text+='User-requested trial package. Current-source correctness checks are in TESTS.json; prior A/B is historical only. Small-request p99 regressed in some trials, duplex and legacy throughput targets were not all met. No full performance, capacity or installed-App/WAN acceptance is claimed.\n\n'
    if untested:
        text+='User explicitly requested direct release without running tests. No unit, race/vet, capacity, performance, scheduler-promotion, DMG runtime, or physical App+Surge test result is claimed for this Source-ID.\n\n'
    text+=('SCHEDULER-MODES.json passed the source-matched release gates. ' if scheduler.get('verified') is True else 'Engineering build: one or more scheduler/performance gates are failed or pending; see SCHEDULER-MODES.json. ') + 'Weighted uses capability revision 5; Auto/Aggregate/Protect retain their 0x41/0x42/0x43 hello values. This is not physical App acceptance.\n\n'
    text+='CAPACITY.json records actual simultaneous logical streams, not HTTP counts. RUNTIME.json records the independent 180-second real App/Surge mixed run. These records are distinct from build provenance.\n\n'
    if capacity.get('verified'):
        text+='| Streams | Round | Seconds | Exchanges | Churn | Segmented bulk |\n|---:|---:|---:|---:|---:|---:|\n'
        for case in capacity['cases']:
            text+=f"| {case['target_streams']} | {case['round']} | {case['seconds']:.3f} | {case['exchanges']} | {case['churn_reopens']} | {case['bulk_segments']} |\n"
        text+='\nAll ten cases preserved the six-carrier session and returned credit/pages/pending to baseline. The explicit 2049th stream refusal is a safety-boundary test, not a normal-load failure.\n\n'
    else:text+='Capacity acceptance: pending.\n\n'
    if runtime.get('verified'):
        text+=f"Physical mixed run: {runtime['observed_seconds']:.3f} seconds; {runtime['short_attempts']} short requests with zero failures; {len(runtime['bulk_segments'])} separately completed bulk segments; {runtime['idle_keepalive_connections']} idle/keepalive connections. Six carriers preserved and payloads independently crosschecked at the origin.\n\n"
    else:text+='Real 180-second App/Surge mixed acceptance: pending. Do not interpret a candidate or laboratory run as a completed physical release.\n\n'
    text+='MPX/3 is incompatible with MPX/2. Weighted (0x44) requires 0.9.4 on both Mac and Landing; 0.9.4 Auto/Aggregate/Protect retain the 0.9.3 hello values. Existing key/Relay/backend models remain unchanged. No multi-day stability, physical Intel, notarization, forward-secrecy or independent security-audit claim.\n'
    return text


def check_preview(tests: dict, scheduler: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed', 'Preview requires current-source correctness evidence')
    gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==3 and scheduler.get('scheduler_capability_revision')==5 and scheduler.get('verified') is False and scheduler.get('status')=='preview-with-known-limitations' and bool(scheduler.get('known_limitations')), 'Preview must explicitly retain unpassed performance gates')
    commands=tests.get('commands',[])
    required={'go-test','go-vet','go-race','warm-seed-race','release-script-tests'}
    gates.require({r.get('name') for r in commands}==required and len(commands)==len(required),'Preview correctness inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name)
        gates.require(name and not path.is_absolute() and '..' not in path.parts, 'Unsafe preview evidence path')
        local=root/path
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'), 'Missing, failed or changed preview evidence: '+name)


def main() -> None:
    parser=argparse.ArgumentParser()
    parser.add_argument('--require-live',action='store_true',help='Require 128/256/512/1024/2048 x 30s x 2 AND the source-matched 180s App/Surge mixed run')
    parser.add_argument('--engineering',action='store_true',help='Package explicitly unpromoted engineering artifacts; never marks failed performance/runtime gates passed')
    parser.add_argument('--untested-release',action='store_true',help='Direct release explicitly marked untested-by-request; bypasses test evidence gates, not source/archive integrity checks')
    parser.add_argument('--preview-release',action='store_true',help='User-requested preview with current correctness/build proof and explicit known limitations; not performance promotion')
    parser.add_argument('--weighted-release',action='store_true',help='Formal 0.9.4 Weighted feature release with current-source correctness and Weighted laboratory gates; does not claim physical App/WAN acceptance')
    args=parser.parse_args()
    gates.require(sum(bool(x) for x in [args.engineering,args.require_live,args.untested_release,args.preview_release,args.weighted_release]) <= 1,'Select at most one packaging mode')
    version=(ROOT/'macos/VERSION').read_text().strip()
    gates.require(version=='0.9.4','This release gate is defined for 0.9.4 / MPX/3 Rev5')
    out=ROOT/'dist'/('userspace-'+version)
    files=source.collect();sums=source.manifest(files);identity=source.sha(sums)
    gates.require((out/'SOURCE_ID').read_text().strip()==identity and (out/'SOURCE_SHA256SUMS').read_bytes()==sums,'Source freeze missing or stale')
    source_name=f'MPTCP-Userspace-{version}-source.tar.gz'
    expected=dict(files,SOURCE_SHA256SUMS=sums,SOURCE_ID=(identity+'\n').encode())
    with tarfile.open(out/source_name,'r:gz') as archive:
        gates.require(set(archive.getnames())==set(expected),'Source archive inventory differs')
        for entry in archive:
            stream=archive.extractfile(entry) if entry.isfile() else None
            gates.require(not entry.pax_headers and stream is not None and stream.read()==expected[entry.name],'Source bytes differ: '+entry.name)
    provenance=json.loads((out/'PROVENANCE.json').read_text())
    gates.require(provenance.get('source_id')==identity and provenance.get('version')==version,'Build provenance does not bind this source')
    if args.untested_release:
        gates.require(provenance.get('verified') is False and provenance.get('status')=='untested-by-request','Untested release provenance must be explicit')
    else:
        gates.require(provenance.get('verified') is True,'Build verification does not bind this source')
    for name,digest in provenance['artifact_sha256'].items():
        gates.require(source.sha((out/name).read_bytes())==digest,'Recorded artifact changed: '+name)
    scheduler=json.loads((out/'SCHEDULER-MODES.json').read_text())
    if args.untested_release:
        gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('verified') is False and scheduler.get('status')=='untested-by-request','Untested scheduler record must be explicit')
    elif args.preview_release:
        tests=json.loads((out/'TESTS.json').read_text())
        check_preview(tests,scheduler,identity,version)
    elif args.weighted_release:
        tests=json.loads((out/'TESTS.json').read_text())
        gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed','Weighted release requires current-source correctness evidence')
        gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==3 and scheduler.get('scheduler_capability_revision')==5 and scheduler.get('verified') is True and scheduler.get('status')=='weighted-release-passed','Weighted scheduler release record is missing or stale')
        gates.require(scheduler.get('configured_modes')==['auto','aggregate','protect','weighted'] and scheduler.get('default_mode')=='auto','Weighted release mode inventory mismatch')
        checks=scheduler.get('checks',{})
        for key in ['directional_authenticated_capacity','upload_blank_auto','penalty_timeout_protection','legacy_modes_regression','weighted_highbdp']:
            gates.require(checks.get(key) is True,'Missing Weighted release check: '+key)
    elif args.engineering:
        gates.require(scheduler.get('source_id') == identity and scheduler.get('version') == version and scheduler.get('checks',{}).get('rev2_credit_and_directional_reset') is True and scheduler.get('checks',{}).get('final_race_vet') is True,'Engineering build requires current source and completed Rev2 correctness/race/vet')
    else:
        scheduler_gates.check(scheduler,identity,verify_evidence=True)
    records=[]
    for name in ['CAPACITY.json','RUNTIME.json']:
        path=out/name
        if not path.exists():
            path.write_text(json.dumps({'version':version,'wire_protocol':3,'source_id':identity,'verified':False,'status':'pending','reason':'Required short acceptance has not been recorded'},indent=2)+'\n')
        records.append(json.loads(path.read_text()))
    capacity,runtime=records
    if args.preview_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('wire_protocol')==3 and r.get('verified') is False and r.get('status')=='not-run-for-preview' for r in [capacity,runtime]),'Preview must not claim capacity/runtime promotion')
        complete=False
    elif args.weighted_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('wire_protocol')==3 and r.get('verified') is False and r.get('status')=='not-run-for-weighted-release' for r in [capacity,runtime]),'Weighted release must keep unrun capacity/runtime evidence explicit')
        complete=False
    elif args.untested_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('verified') is False and r.get('status')=='untested-by-request' for r in [capacity,runtime]),'Untested release records must be explicit')
        complete=False
    else:
        gates.require(capacity.get("verified") is True,"New candidate requires source-matched formal capacity")
        complete=gates.check(capacity,runtime,identity,required=args.require_live)
        if args.engineering: complete=False
    (out/'ACCEPTANCE.md').write_text(acceptance_text(identity,capacity,runtime,complete,scheduler,version=version,untested=args.untested_release,preview=args.preview_release,weighted_release=args.weighted_release))
    names=[f'MPTCP-Desk-{version}-universal.dmg','mptcp-landing','mptcp-landing.sha256','mptcp-landing.BUILDINFO',
           'MPTCP-Desk.BUILDINFO',source_name,'SOURCE_ID','SOURCE_SHA256SUMS','PROVENANCE.json','CAPACITY.json','RUNTIME.json','SCHEDULER-MODES.json','REV2-AB.json','ACCEPTANCE.md']
    if args.preview_release or args.weighted_release:
        names.append('TESTS.json')
    release={name:(out/name).read_bytes() for name in names}
    for title,name in [('README.zh-CN.md','RELEASE.zh-CN.md'),('DEPLOYMENT.zh-CN.md','DEPLOYMENT.zh-CN.md'),
                       ('VALIDATION.md','VALIDATION.md'),('PROTOCOL.md','PROTOCOL.md'),
                       ('ADAPTIVE-FLOW-CONTROL.md','ADAPTIVE-FLOW-CONTROL.md'),('MPX3-CREDIT.md','MPX3-CREDIT.md'),('SCHEDULER-MODES.md','SCHEDULER-MODES.md'),('REV2-SHARED-CREDIT.md','REV2-SHARED-CREDIT.md')]:
        release[title]=files['docs/userspace/'+name]
    go_path=ROOT/'macos/build/go-path'
    go=os.environ.get('MPTCP_GO') or (go_path.read_text().strip() if go_path.exists() else shutil.which('go'))
    gates.require(bool(go),'Set MPTCP_GO to the actual compiler')
    goroot=subprocess.check_output([go,'env','GOROOT'],text=True).strip()
    release['GO-LICENSE.txt']=(pathlib.Path(goroot)/'LICENSE').read_bytes()
    secrets=local_test_secrets()
    for name,data in list(files.items())+list(release.items()):
        gates.require(not any(secret in data for secret in secrets),'Disposable test secret in release: '+name)
    release['SHA256SUMS']=source.manifest(release)
    bundle=out/f'MPTCP-Userspace-{version}-{"preview" if args.preview_release else ("engineering" if args.engineering else "release")}.tar.gz';source.archive(bundle,release)
    gates.require(source.manifest(source.collect())==sums,'Sources changed during packaging')
    external={name:(out/name).read_bytes() for name in names+[bundle.name]}
    (out/f'MPTCP-Userspace-{version}-SHA256SUMS').write_bytes(source.manifest(external))
    print(json.dumps({'version':version,'wire_protocol':3,'source_id':identity,
        'release_stage':'weighted-feature-release' if args.weighted_release else ('preview-with-known-limitations' if args.preview_release else 'untested-by-request' if args.untested_release else ('engineering-not-release-gated' if args.engineering else ('short-capacity-and-physical-validated' if complete else 'candidate-pending-required-acceptance'))),
        'scheduler_verified':scheduler.get('verified') is True,'capacity_verified':capacity.get('verified') is True,'runtime_verified':runtime.get('verified') is True,
        'source_files':len(files),'release_files':len(release),'archive':bundle.name,
        'archive_bytes':bundle.stat().st_size,'archive_sha256':source.sha(bundle.read_bytes()),
        'checks':['frozen source including untracked files','binary provenance hashes','explicit release allowlist',
                  'exact disposable-secret exclusion','archive re-read and byte comparison','separate short capacity and physical gates'],
        'limits':('Formal Weighted feature release; source-matched correctness and Weighted lab gates only; no full capacity/physical App/WAN acceptance' if args.weighted_release else ('User-requested preview; known latency/throughput limitations; no full performance/capacity/WAN acceptance' if args.preview_release else 'User-requested direct release without tests; no test validation claim' if args.untested_release else 'No anonymous publication; no multi-day/physical-Intel/security certification'))},indent=2))

if __name__=='__main__':main()
