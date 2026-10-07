#!/usr/bin/env python3
"""Package frozen MPTCP Userspace artifacts, including MPX/4 Stable and Provisioning."""
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
STABLE_VERSION='1.0.0'
STABLE_PROTOCOL_RELEASE='protocol-v4.0.0'
STABLE_PROTOCOL_SOURCE='44f587fd279ed2238b070dd68114c76822353f4d'
STABLE_BROKER_SHA256='5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9'
STABLE_CORE_VECTORS=(
    'carrier-generation.json','close-ordering.json','confirmation-validity.json','error-scope.json',
    'frame-encoding.json','handshake-ambiguity.json','handshake-reject.json','identity-lifecycle.json',
    'key-schedule.json','max-carriers.json','recovery-progress.json','reordering-reliability.json',
    'secure-record.json','session-lifecycle.json','state-validity.json','tcp-binding.json',
    'terminal-flow-control.json','transmission-allocation.json','varint.json','version-compatibility.json',
)


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


def acceptance_text(identity: str, capacity: dict, runtime: dict, complete: bool, scheduler: dict, *, version: str, untested: bool=False, preview: bool=False, background_release: bool=False, feature_release: bool=False, stable_release: bool=False, stable_candidate: bool=False, stable_patch: bool=False) -> str:
    stable_protocol = stable_release or stable_candidate or version.startswith('1.')
    protocol='MPX/4 Protocol Version 4 Stable' if stable_protocol else 'MPX/4 Draft 04'
    text=f'# {version} / {protocol} acceptance\n\n'
    stage='stable-protocol-v4-release' if stable_release else ('stable-protocol-v4-candidate' if stable_candidate else ('stable-protocol-v4-implementation-release' if stable_patch else ('control-plane-feature-release' if feature_release else ('background-resident-feature-release' if background_release else ('preview-with-known-limitations' if preview else ('untested-by-request release' if untested else ('short-capacity-and-physical-validated' if complete else 'candidate; required acceptance pending')))))))
    text+=f'Source-ID: `{identity}`. Stage: **{stage}**.\n\n'
    if stable_protocol:
        text+=f'This release uses MPX/4 Protocol Version 4 Stable, frozen by {STABLE_PROTOCOL_RELEASE} at {STABLE_PROTOCOL_SOURCE}. Core scheduler negotiation is removed; Auto/Aggregate/Protect/Weighted are endpoint-local policies and Weighted may use the published RECEIVE_CAPACITY_HINT extension. Release records bind to this Source-ID; untested patch releases explicitly make no test or performance acceptance claim.\n\n'
    if feature_release:
        text+='This 0.10.x feature/patch release changes client/runtime/control-plane behavior above the unchanged MPX/4 Draft 04 transport. Current-source correctness/build evidence is recorded in TESTS.json and PROVENANCE.json. Scheduler/capacity/WAN performance promotion is intentionally not claimed for this Source-ID because the transport wire and multipath scheduler semantics are unchanged.\n\n'
    if background_release:
        text+='Formal 0.9.8 release candidate. Current-source correctness includes login-item API compilation, sleep/wake recovery policy, bounded restart backoff, Swift UI/Profile checks, the unchanged Rev5/Weighted protocol regression and source-matched Weighted laboratory throughput. Full 30-second capacity matrices and physical App+Surge/WAN acceptance are not claimed unless separately verified.\n\n'
    if preview:
        text+='User-requested trial package. Current-source correctness checks are in TESTS.json; prior A/B is historical only. Small-request p99 regressed in some trials, duplex and legacy throughput targets were not all met. No full performance, capacity or installed-App/WAN acceptance is claimed.\n\n'
    if untested:
        text+='User explicitly requested direct release without running tests. No unit, race/vet, capacity, performance, scheduler-promotion, DMG runtime, or physical App+Surge test result is claimed for this Source-ID.\n\n'
    scheduler_note='Scheduler laboratory evidence uses local scheduler policy revision 6; scheduler mode is not MPX/4 Core wire state.' if stable_protocol else 'Scheduler laboratory evidence uses scheduler capability revision 5; the transport wire is MPX/4 Draft 04.'
    text+=('SCHEDULER-MODES.json passed the source-matched release gates. ' if scheduler.get('verified') is True else 'Engineering build: one or more scheduler/performance gates are failed or pending; see SCHEDULER-MODES.json. ') + scheduler_note + ' This is not physical App acceptance.\n\n'
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
    if stable_release:
        text+=f'{version} is released as a matched Desk/Linux Client/Landing/Provisioning suite. MPX/4 Protocol Version remains 4, but pre-Stable Draft 04 peers are not a supported same-port fallback because their Version-4 handshake semantics differ. The frozen Keychain Broker remains v1 sha256={STABLE_BROKER_SHA256}. No multi-day stability, physical Intel, notarization, forward-secrecy or independent security-audit claim.\n'
    elif stable_candidate:
        text+=f'{version} is prepared as a matched MPX/4 Stable candidate. Physical App/Surge runtime evidence was intentionally omitted by request and remains pending; this artifact makes no production-runtime or WAN acceptance claim. Pre-Stable Draft 04 peers are not a supported same-port fallback because their Version-4 handshake semantics differ. The frozen Keychain Broker remains v1 sha256={STABLE_BROKER_SHA256}.\n'
    elif stable_patch:
        text+=f'{version} is released as a matched Desk/Linux Client/Landing/Provisioning MPX/4 Protocol Version 4 Stable implementation release. Current-source correctness/build evidence passed, while scheduler performance promotion, capacity and physical WAN/App acceptance were not rerun and are not claimed. The frozen Keychain Broker remains v1 sha256={STABLE_BROKER_SHA256}.\n'
    elif stable_protocol:
        text+=f'{version} is released as a matched Desk/Linux Client/Landing/Provisioning MPX/4 Protocol Version 4 Stable patch. Tests and performance acceptance were intentionally not rerun by user request; no new validation claim is made. The frozen Keychain Broker remains v1 sha256={STABLE_BROKER_SHA256}.\n'
    else:
        text+=f'MPX/4 Draft 04 is incompatible with the MPX/3 transport used by 0.9.5 and older. {version} is released as a matched Desk/Linux Client/Landing/Provisioning suite. Existing MPX/4 key schedule and wire registry remain unchanged. No multi-day stability, physical Intel, notarization, forward-secrecy or independent security-audit claim.\n'
    return text


def check_preview(tests: dict, scheduler: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed', 'Preview requires current-source correctness evidence')
    gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==4 and scheduler.get('scheduler_capability_revision')==5 and scheduler.get('verified') is False and scheduler.get('status')=='preview-with-known-limitations' and bool(scheduler.get('known_limitations')), 'Preview must explicitly retain unpassed performance gates')
    commands=tests.get('commands',[])
    required={'go-test','go-vet','go-race','warm-seed-race','release-script-tests'}
    gates.require({r.get('name') for r in commands}==required and len(commands)==len(required),'Preview correctness inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name)
        gates.require(name and not path.is_absolute() and '..' not in path.parts, 'Unsafe preview evidence path')
        local=root/path
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'), 'Missing, failed or changed preview evidence: '+name)


BACKGROUND_REQUIRED={'lifecycle-policy','swift-typecheck','swift-ui','scheduler-ui','go-test','go-vet','go-race','weighted-release','release-script-tests'}

def check_background_release(tests: dict, scheduler: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed','Background release requires current-source correctness evidence')
    commands=tests.get('commands',[])
    names=[r.get('name') for r in commands]
    gates.require(BACKGROUND_REQUIRED.issubset(set(names)) and len(names)==len(set(names)),'Background release test inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name)
        gates.require(name and not path.is_absolute() and '..' not in path.parts,'Unsafe background release evidence path')
        local=root/path
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'),'Missing, failed or changed background release evidence: '+name)
    gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==4 and scheduler.get('scheduler_capability_revision')==5 and scheduler.get('verified') is True and scheduler.get('status')=='background-release-passed','Scheduler/background release record is missing or stale')
    gates.require(scheduler.get('configured_modes')==['auto','aggregate','protect','weighted'] and scheduler.get('default_mode')=='auto','Background release mode inventory mismatch')
    checks=scheduler.get('checks',{})
    for key in ['directional_authenticated_capacity','upload_blank_auto','penalty_timeout_protection','legacy_modes_regression','weighted_highbdp','background_resident_policy']:
        gates.require(checks.get(key) is True,'Missing background release check: '+key)


FEATURE_REQUIRED={'go-test','go-vet','go-race','provisioning-test','provisioning-vet','swift-typecheck-arm64','swift-typecheck-x86_64','bundle-api-tests','bundle-engine-tests','linux-amd64-runtime'}
STABLE_PATCH_REQUIRED={'go-test-all','go-vet','go-race-pending'}
STABLE_REQUIRED=FEATURE_REQUIRED|{'mpx4-stable-core','broker-source-freeze','release-script-tests'}
STABLE_CANDIDATE_REQUIRED=STABLE_REQUIRED-{'linux-amd64-runtime'}

def check_stable_candidate(tests: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(version==STABLE_VERSION and (ROOT/'provisioning/VERSION').read_text().strip()==version,'Stable candidate requires one 1.0.0 suite version')
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='static-build-passed','Stable candidate requires current-source static/build evidence')
    commands=tests.get('commands',[]); names=[r.get('name') for r in commands]
    gates.require(STABLE_CANDIDATE_REQUIRED.issubset(set(names)) and len(names)==len(set(names)),'Stable candidate test inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name); local=root/path
        gates.require(name and not path.is_absolute() and '..' not in path.parts,'Unsafe stable-candidate evidence path')
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'),'Missing, failed or changed stable-candidate evidence: '+name)

def check_stable_patch(tests: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(version.startswith('1.') and version!=STABLE_VERSION and (ROOT/'provisioning/VERSION').read_text().strip()==version,'Stable implementation release requires one matched 1.x suite version')
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed','Stable implementation release requires current-source correctness evidence')
    commands=tests.get('commands',[]); names=[r.get('name') for r in commands]
    gates.require(STABLE_PATCH_REQUIRED.issubset(set(names)) and len(names)==len(set(names)),'Stable implementation release test inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name); local=root/path
        gates.require(name and not path.is_absolute() and '..' not in path.parts,'Unsafe stable-implementation evidence path')
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'),'Missing, failed or changed stable-implementation evidence: '+name)


def check_feature_release(tests: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed','Feature release requires current-source correctness evidence')
    commands=tests.get('commands',[]); names=[r.get('name') for r in commands]
    gates.require(FEATURE_REQUIRED.issubset(set(names)) and len(names)==len(set(names)),'Feature release test inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name)
        gates.require(name and not path.is_absolute() and '..' not in path.parts,'Unsafe feature-release evidence path')
        local=root/path
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'),'Missing, failed or changed feature-release evidence: '+name)


def check_stable_release(tests: dict, scheduler: dict, identity: str, version: str, *, root: pathlib.Path=ROOT) -> None:
    gates.require(version==STABLE_VERSION and (ROOT/'provisioning/VERSION').read_text().strip()==version,'Stable release requires one 1.0.0 suite version')
    gates.require(tests.get('source_id')==identity and tests.get('version')==version and tests.get('verified') is True and tests.get('status')=='correctness-passed','Stable release requires current-source correctness evidence')
    commands=tests.get('commands',[]); names=[r.get('name') for r in commands]
    gates.require(STABLE_REQUIRED.issubset(set(names)) and len(names)==len(set(names)),'Stable release test inventory missing or duplicated')
    for record in commands:
        name=record.get('log',''); path=pathlib.PurePosixPath(name); local=root/path
        gates.require(name and not path.is_absolute() and '..' not in path.parts,'Unsafe stable-release evidence path')
        gates.require(record.get('exit_code')==0 and local.is_file() and not local.is_symlink() and local.resolve().is_relative_to(root.resolve()) and source.sha(local.read_bytes())==record.get('sha256'),'Missing, failed or changed stable-release evidence: '+name)
    scheduler_gates.check(scheduler,identity,verify_evidence=True,root=root)
    vector_dir=ROOT/'macos/engine/multipath/testdata'
    gates.require(tuple(sorted(p.name for p in vector_dir.glob('*.json')))==tuple(sorted(STABLE_CORE_VECTORS)),'Stable Core vector inventory differs from protocol-v4.0.0')
    for name in STABLE_CORE_VECTORS:
        vector=json.loads((vector_dir/name).read_text())
        gates.require(vector.get('protocol')=='MPX/4' and vector.get('revision')=='Draft 11','Stable Core vector metadata differs: '+name)
    hint=json.loads((vector_dir/'extensions/capacity-hint.json').read_text())
    gates.require(hint.get('parameter_type')=='0x40' and hint.get('core_revision')=='Draft 11','Capacity Hint extension vector differs')
    import base64
    broker=base64.b64decode(b''.join((ROOT/'macos/keychain-broker/MPTCPKeychainBroker.v1.b64').read_bytes().split()),validate=True)
    gates.require(source.sha(broker)==STABLE_BROKER_SHA256,'Frozen Keychain Broker v1 bytes changed')


def main() -> None:
    parser=argparse.ArgumentParser()
    parser.add_argument('--require-live',action='store_true',help='Require 128/256/512/1024/2048 x 30s x 2 AND the source-matched 180s App/Surge mixed run')
    parser.add_argument('--engineering',action='store_true',help='Package explicitly unpromoted engineering artifacts; never marks failed performance/runtime gates passed')
    parser.add_argument('--untested-release',action='store_true',help='Direct release explicitly marked untested-by-request; bypasses test evidence gates, not source/archive integrity checks')
    parser.add_argument('--preview-release',action='store_true',help='User-requested preview with current correctness/build proof and explicit known limitations; not performance promotion')
    parser.add_argument('--background-release',action='store_true',help='Formal 0.9.8 lifecycle/correctness release evidence mode')
    parser.add_argument('--feature-release',action='store_true',help='0.10.x feature/patch release with current-source correctness/build proof; does not claim scheduler/capacity promotion')
    parser.add_argument('--stable-release',action='store_true',help='Formal 1.0.0 MPX/4 Protocol Version 4 Stable matched-suite release')
    parser.add_argument('--stable-candidate',action='store_true',help='1.0.0 MPX/4 Stable candidate with static/build proof and runtime explicitly pending')
    parser.add_argument('--stable-patch',action='store_true',help='Post-1.0.0 Stable implementation release with current correctness/build proof; no new performance/WAN promotion')
    args=parser.parse_args()
    gates.require(sum(bool(x) for x in [args.engineering,args.require_live,args.untested_release,args.preview_release,args.background_release,args.feature_release,args.stable_release,args.stable_candidate,args.stable_patch]) <= 1,'Select at most one packaging mode')
    version=(ROOT/'macos/VERSION').read_text().strip()
    gates.require(version in {'0.9.8','0.10.0','0.10.1','0.10.2','0.10.3','0.10.4','0.10.5','0.10.6','0.10.7','0.10.8','0.10.9','0.10.10','0.10.11','0.10.12','1.0.0','1.0.1','1.0.2','1.0.3','1.0.4','1.0.5','1.1.0','1.1.1'},'Unsupported release version for this packaging script')
    if args.feature_release: gates.require(version.startswith('0.10.'),'--feature-release is defined for 0.10.x')
    if args.stable_release: gates.require(version==STABLE_VERSION,'--stable-release is defined for 1.0.0')
    if args.stable_candidate: gates.require(version==STABLE_VERSION,'--stable-candidate is defined for 1.0.0')
    if args.stable_patch: gates.require(version.startswith('1.') and version!=STABLE_VERSION,'--stable-patch is defined for post-1.0.0 Stable implementation releases')
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
    scheduler_path=out/'SCHEDULER-MODES.json'
    if args.feature_release and not scheduler_path.exists():
        scheduler_path.write_text(json.dumps({'version':version,'wire_protocol':4,'source_id':identity,'scheduler_capability_revision':5,'verified':False,'status':'not-rerun-for-control-plane-feature-release','reason':version+' changes client/runtime/control-plane or UI behavior; MPX/4 Draft 04 scheduler semantics are unchanged and no performance-promotion claim is made'},indent=2)+'\n')
    scheduler=json.loads(scheduler_path.read_text())
    if args.stable_release:
        tests=json.loads((out/'TESTS.json').read_text()); check_stable_release(tests,scheduler,identity,version)
        for name in ['MPTCP-Desk.BUILDINFO','mptcp-client-linux-amd64.BUILDINFO','mptcp-client-linux-arm64.BUILDINFO','mptcp-landing.BUILDINFO','mptcp-landing-linux-arm64.BUILDINFO']:
            info=(out/name).read_text()
            gates.require('Protocol: MPX/4 Protocol Version 4 Stable' in info and 'Protocol-Release: '+STABLE_PROTOCOL_RELEASE in info and 'Protocol-Source: '+STABLE_PROTOCOL_SOURCE in info,'Stable protocol BUILDINFO missing: '+name)
        gates.require('Keychain-Broker: v1 sha256='+STABLE_BROKER_SHA256 in (out/'MPTCP-Desk.BUILDINFO').read_text(),'Stable Broker BUILDINFO missing')
    elif args.stable_candidate:
        tests=json.loads((out/'TESTS.json').read_text()); check_stable_candidate(tests,identity,version)
        scheduler_gates.check(scheduler,identity,verify_evidence=True)
    elif args.stable_patch:
        tests=json.loads((out/'TESTS.json').read_text()); check_stable_patch(tests,identity,version)
        gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==4 and scheduler.get('verified') is False and scheduler.get('status')=='not-rerun-for-stable-implementation-release','Stable implementation scheduler record must avoid a performance-promotion claim')
    elif args.feature_release:
        gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('wire_protocol')==4 and scheduler.get('verified') is False and scheduler.get('status')=='not-rerun-for-control-plane-feature-release','Feature release scheduler record must explicitly avoid a performance-promotion claim')
        tests=json.loads((out/'TESTS.json').read_text()); check_feature_release(tests,identity,version)
    elif args.untested_release:
        gates.require(scheduler.get('source_id')==identity and scheduler.get('version')==version and scheduler.get('verified') is False and scheduler.get('status')=='untested-by-request','Untested scheduler record must be explicit')
    elif args.preview_release:
        tests=json.loads((out/'TESTS.json').read_text())
        check_preview(tests,scheduler,identity,version)
    elif args.background_release:
        tests=json.loads((out/'TESTS.json').read_text())
        check_background_release(tests,scheduler,identity,version)
    elif args.engineering:
        gates.require(scheduler.get('source_id') == identity and scheduler.get('version') == version and scheduler.get('checks',{}).get('rev2_credit_and_directional_reset') is True and scheduler.get('checks',{}).get('final_race_vet') is True,'Engineering build requires current source and completed Rev2 correctness/race/vet')
    else:
        scheduler_gates.check(scheduler,identity,verify_evidence=True)
    records=[]
    for name in ['CAPACITY.json','RUNTIME.json']:
        path=out/name
        if not path.exists():
            path.write_text(json.dumps({'version':version,'wire_protocol':4,'source_id':identity,'verified':False,'status':'pending','reason':'Required short acceptance has not been recorded'},indent=2)+'\n')
        records.append(json.loads(path.read_text()))
    capacity,runtime=records
    if args.stable_release:
        gates.require(capacity.get('verified') is True and runtime.get('verified') is True,'Stable 1.0.0 requires fresh capacity and physical runtime evidence')
        complete=gates.check(capacity,runtime,identity,required=True)
    elif args.stable_candidate:
        gates.require(capacity.get('verified') is True and capacity.get('source_id')==identity,'Stable candidate requires source-matched capacity evidence')
        gates.check(capacity,runtime,identity,required=False)
        gates.require(runtime.get('verified') is False and runtime.get('status')=='not-run-by-request','Stable candidate must mark physical runtime as intentionally omitted')
        complete=False
    elif args.stable_patch:
        for path,label in [(out/'CAPACITY.json','capacity'),(out/'RUNTIME.json','runtime')]:
            path.write_text(json.dumps({'version':version,'wire_protocol':4,'source_id':identity,'verified':False,'status':'not-rerun-for-stable-implementation-release','reason':version+' is a Stable implementation release with current correctness/build evidence; no new performance/capacity/WAN promotion is claimed'},indent=2)+'\n')
        capacity=json.loads((out/'CAPACITY.json').read_text());runtime=json.loads((out/'RUNTIME.json').read_text())
        complete=False
    elif args.feature_release:
        for path,label in [(out/'CAPACITY.json','capacity'),(out/'RUNTIME.json','runtime')]:
            path.write_text(json.dumps({'version':version,'wire_protocol':4,'source_id':identity,'verified':False,'status':'not-rerun-for-control-plane-feature-release','reason':version+' changes control-plane/client behavior only; no new transport performance/WAN promotion is claimed'},indent=2)+'\n')
        capacity=json.loads((out/'CAPACITY.json').read_text());runtime=json.loads((out/'RUNTIME.json').read_text())
        complete=False
    elif args.preview_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('wire_protocol')==4 and r.get('verified') is False and r.get('status')=='not-run-for-preview' for r in [capacity,runtime]),'Preview must not claim capacity/runtime promotion')
        complete=False
    elif args.background_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('wire_protocol')==4 and r.get('verified') is False and r.get('status')=='not-run-for-background-release' for r in [capacity,runtime]),'Background release must keep unrun capacity/runtime evidence explicit')
        complete=False
    elif args.untested_release:
        gates.require(all(r.get('source_id')==identity and r.get('version')==version and r.get('verified') is False and r.get('status')=='untested-by-request' for r in [capacity,runtime]),'Untested release records must be explicit')
        complete=False
    else:
        gates.require(capacity.get("verified") is True,"New candidate requires source-matched formal capacity")
        complete=gates.check(capacity,runtime,identity,required=args.require_live)
        if args.engineering: complete=False
    (out/'ACCEPTANCE.md').write_text(acceptance_text(identity,capacity,runtime,complete,scheduler,version=version,untested=args.untested_release,preview=args.preview_release,background_release=args.background_release,feature_release=args.feature_release,stable_release=args.stable_release,stable_candidate=args.stable_candidate,stable_patch=args.stable_patch))
    names=[f'MPTCP-Desk-{version}-universal.dmg','appcast.xml',
           'mptcp-client-linux-amd64','mptcp-client-linux-amd64.sha256','mptcp-client-linux-amd64.BUILDINFO',
           'mptcp-client-linux-arm64','mptcp-client-linux-arm64.sha256','mptcp-client-linux-arm64.BUILDINFO',
           'mptcp-landing','mptcp-landing.sha256','mptcp-landing.BUILDINFO',
           'mptcp-landing-linux-arm64','mptcp-landing-linux-arm64.sha256','mptcp-landing-linux-arm64.BUILDINFO',
           'mpx-provision','mpx-provision.sha256','mpx-provision.BUILDINFO',
           'mpx-provision-linux-arm64','mpx-provision-linux-arm64.sha256','mpx-provision-linux-arm64.BUILDINFO',
           'MPTCP-Desk.BUILDINFO',source_name,'SOURCE_ID','SOURCE_SHA256SUMS','PROVENANCE.json','CAPACITY.json','RUNTIME.json','SCHEDULER-MODES.json','ACCEPTANCE.md']
    if not (args.feature_release or args.untested_release or args.stable_patch):
        names.append('REV2-AB.json')
    if args.preview_release or args.background_release or args.feature_release or args.stable_release or args.stable_candidate or args.stable_patch:
        names.append('TESTS.json')
    release={name:(out/name).read_bytes() for name in names}
    for title,name in [('README.zh-CN.md','RELEASE.zh-CN.md'),('PROVISIONING.md','PROVISIONING.md'),('DEPLOYMENT.zh-CN.md','DEPLOYMENT.zh-CN.md'),
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
    bundle_stage='stable-candidate' if args.stable_candidate else ('preview' if args.preview_release else ('engineering' if args.engineering else 'release'))
    bundle=out/f'MPTCP-Userspace-{version}-{bundle_stage}.tar.gz';source.archive(bundle,release)
    gates.require(source.manifest(source.collect())==sums,'Sources changed during packaging')
    external={name:(out/name).read_bytes() for name in names+[bundle.name]}
    (out/f'MPTCP-Userspace-{version}-SHA256SUMS').write_bytes(source.manifest(external))
    print(json.dumps({'version':version,'wire_protocol':4,'source_id':identity,
        'release_stage':'stable-protocol-v4-release' if args.stable_release else ('stable-protocol-v4-candidate' if args.stable_candidate else ('stable-protocol-v4-implementation-release' if args.stable_patch else ('control-plane-feature-release' if args.feature_release else ('background-resident-feature-release' if args.background_release else ('preview-with-known-limitations' if args.preview_release else 'untested-by-request' if args.untested_release else ('engineering-not-release-gated' if args.engineering else ('short-capacity-and-physical-validated' if complete else 'candidate-pending-required-acceptance'))))))),
        'scheduler_verified':scheduler.get('verified') is True,'capacity_verified':capacity.get('verified') is True,'runtime_verified':runtime.get('verified') is True,
        'source_files':len(files),'release_files':len(release),'archive':bundle.name,
        'archive_bytes':bundle.stat().st_size,'archive_sha256':source.sha(bundle.read_bytes()),
        'checks':['frozen source including untracked files','binary provenance hashes','explicit release allowlist',
                  'exact disposable-secret exclusion','archive re-read and byte comparison','separate short capacity and physical gates'],
        'limits':('Formal background-resident feature release; source-matched lifecycle/correctness and unchanged Weighted/Rev5 lab gates only; no full capacity/physical App/WAN acceptance' if args.background_release else ('User-requested preview; known latency/throughput limitations; no full performance/capacity/WAN acceptance' if args.preview_release else 'User-requested direct release without tests; no test validation claim' if args.untested_release else 'No anonymous publication; no multi-day/physical-Intel/security certification'))},indent=2))

if __name__=='__main__':main()
