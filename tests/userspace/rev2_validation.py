#!/usr/bin/env python3
"""Source-bound, opt-in 0.9.0 validation. No production service or App changes.

The A/B primary statistic is the ratio of medians for two reversed-order
repetitions per history count. Every individual ratio/error remains reported;
a median pass is not a claim that every individual pair reached 95 percent.
The payload/FIN timer and original 240/300 Mbps high-BDP gates are unchanged.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / 'reports/userspace-0.9.0/validation'


def identity(root: Path = ROOT) -> str:
    return subprocess.check_output(['python3', str(root/'scripts/source-manifest.py'), '--id'], text=True).strip()


def run_case(name: str, args: list[str], env: dict[str, str], timeout: int, *, source: Path = ROOT) -> int:
    OUT.mkdir(parents=True, exist_ok=True)
    sid = identity(source)
    log = OUT/(name+'.log')
    receipt = OUT/(name+'.receipt.json')
    if log.exists() or receipt.exists():
        raise RuntimeError('Refusing to replace earlier evidence: '+name)
    started = time.time()
    error = None
    with log.open('w') as stream:
        try:
            process = subprocess.Popen(args, cwd=source, env=dict(os.environ, **env), stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
            code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            code, error = 124, 'validation command exceeded its explicit deadline'
    if '-run' in args and '--- PASS:' not in log.read_text(errors='replace'):
        code, error = 126, 'targeted test did not execute a passing test; skips are not verification'
    current = identity(source)
    record = {'name': name, 'source_id': sid, 'source_after': current,
              'source_unchanged': sid == current, 'exit_code': code,
              'started_unix': started, 'elapsed_seconds': time.time()-started,
              'argv': args, 'environment': env, 'log_sha256': hashlib.sha256(log.read_bytes()).hexdigest()}
    if error:
        record['error'] = error
    receipt.write_text(json.dumps(record, indent=2)+'\n')
    for line in log.read_text(errors='replace').splitlines():
        if any(word in line for word in ['HIGH_BDP ', 'CAPACITY streams=', 'SHARED_CREDIT ', '--- FAIL:', 'panic:', 'PASS: three policies']):
            print(line[:500], flush=True)
    print(f'{name}: exit={code} source_unchanged={sid == current} seconds={record["elapsed_seconds"]:.2f}', flush=True)
    return code if sid == current else 125


def go_test(name: str, pattern: str, *, variables: dict[str, str] | None = None,
            race: bool = False, timeout: int = 150, source: Path = ROOT, packages: str = './multipath') -> int:
    go = os.environ.get('MPTCP_GO')
    if not go:
        raise RuntimeError('MPTCP_GO must name the pinned, verified toolchain')
    sid = identity(source)
    env = {'GOTOOLCHAIN': 'local', 'GOCACHE': '/tmp/mptcp-090-cache', 'GOMODCACHE': '/tmp/mptcp-090-mod'}
    env.update(variables or {})
    args = [go, '-C', 'macos/engine', 'test']
    if race:
        args.append('-race')
    args += [packages, '-run', pattern, '-count=1', f'-timeout={timeout}s', '-ldflags=-X mptcp-desktop/engine/multipath.SourceID='+sid, '-v']
    return run_case(name, args, env, timeout+15, source=source)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('phase', choices=['units', 'ab', 'performance', 'capacity', 'ui'])
    parser.add_argument('--baseline', type=Path)
    args = parser.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    codes = []
    if args.phase == 'units':
        stdin = OUT/'stdin';stdin.mkdir(exist_ok=True)
        codes.append(go_test('final-race', '.', race=True, timeout=160, packages='./...', variables={'MPX_ENGINE_MODES_REPORT': str(stdin)}))
        go = os.environ['MPTCP_GO']
        codes.append(run_case('final-vet', [go, '-C', 'macos/engine', 'vet', './...'], {'GOTOOLCHAIN': 'local', 'GOCACHE': '/tmp/mptcp-090-cache'}, 90))
        codes.append(run_case('release-gate-units', ['python3', '-m', 'unittest', 'discover', '-s', 'tests/userspace', '-p', 'test_*gates.py'], {}, 30))
        for mode in ['auto', 'aggregate', 'protect']:
            directory = OUT/('capacity-smoke-'+mode);directory.mkdir(exist_ok=True)
            codes.append(go_test('capacity-smoke-'+mode, '^TestShortCapacityMatrix$', race=True, timeout=65,
                                 variables={'MPX_CAPACITY':'smoke','MPX_TEST_SCHEDULER':mode,'MPX_CAPACITY_REPORT':str(directory)}))
    elif args.phase == 'ab':
        for round_number in [1, 2]:
            for count in [0, 8, 16, 32]:
                variants = [('b', ROOT)]
                if args.baseline and count in [0, 16]:
                    variants = [('a', args.baseline.resolve())] + variants
                for variant, root in variants:
                    name = f'credit-{variant}-{count}-{round_number}'
                    codes.append(go_test(name, '^TestSharedCreditIdleAcceptance$', timeout=330, source=root,
                                         variables={'MPX_SHARED_CREDIT_AB':'1','MPX_SHARED_CREDIT_COUNTS':str(count),
                                                    'MPX_SHARED_CREDIT_REVERSE':str(round_number-1),'MPX_SHARED_CREDIT_REPORT':str(OUT/(name+'.json'))}))
    elif args.phase == 'performance':
        for mode in ['aggregate', 'auto']:
            for repeat in range(1, 6):
                names = ['300Mbps-6paths-50ms', '500Mbps-6paths-50ms']
                if repeat <= 3:
                    names.append('300Mbps-2paths-50ms')
                name = f'{mode}-uniform-{repeat}'
                codes.append(go_test(name, '^TestHighBDPMatrix$/^('+'|'.join(names)+')$', timeout=90,
                                     variables={'MPX_HIGH_BDP':'enforce','MPX_TEST_SCHEDULER':mode,'MPX_HIGH_BDP_REPORT':str(OUT/(name+'.json'))}))
        for mode, round_number in [('auto',1),('auto',2),('aggregate',1)]:
            name=f'{mode}-full-{round_number}'
            codes.append(go_test(name,'^TestHighBDPMatrix$',timeout=220,variables={'MPX_HIGH_BDP':'enforce','MPX_TEST_SCHEDULER':mode,'MPX_HIGH_BDP_REPORT':str(OUT/(name+'.json'))}))
        for mode in ['protect','auto']:
            for repeat in [1,2,3]:
                name=f'{mode}-asymmetric-{repeat}'
                codes.append(go_test(name,'^TestHighBDPMatrix$/^asymmetric',timeout=90,variables={'MPX_HIGH_BDP':'enforce','MPX_TEST_SCHEDULER':mode,'MPX_HIGH_BDP_REPORT':str(OUT/(name+'.json'))}))
            for repeat in [1,2,3,4,5]:
                name=f'{mode}-extreme-{repeat}'
                codes.append(go_test(name,'^TestSchedulerExtremePathProtection$',timeout=90,variables={'MPX_TEST_SCHEDULER':mode,'MPX_SCHEDULER_EXTREME_REPORT':str(OUT/(name+'.json'))}))
    elif args.phase == 'capacity':
        directory=OUT/'capacity-formal';directory.mkdir(exist_ok=True)
        codes.append(go_test('capacity-formal','^TestShortCapacityMatrix$',timeout=360,variables={'MPX_CAPACITY':'enforce','MPX_TEST_SCHEDULER':'auto','MPX_CAPACITY_REPORT':str(directory)}))
    elif args.phase == 'ui':
        for arch in ['arm64','x86_64']:
            binary=ROOT/'macos/build'/('SchedulerUIHarness-090-'+arch);binary.parent.mkdir(parents=True,exist_ok=True)
            codes.append(run_case('ui-build-'+arch,['xcrun','swiftc','-O','-swift-version','5','-parse-as-library','-D','UI_TEST','-target',arch+'-apple-macosx13.0','-module-cache-path','/tmp/mptcp-swift-cache','macos/Profile.swift','macos/App.swift','tests/userspace/SchedulerUIHarness.swift','-o',str(binary)],{},90))
            directory=OUT/('ui-'+arch)
            codes.append(run_case('ui-run-'+arch,['/usr/bin/arch','-'+arch,str(binary),str(directory),str(OUT/'stdin')],{},45))
    summary={'phase':args.phase,'source_id':identity(),'command_exit_codes':codes,'all_commands_succeeded':all(c==0 for c in codes)}
    path=OUT/(args.phase+'-commands.json')
    if path.exists():
        raise RuntimeError('Refusing to overwrite phase summary')
    path.write_text(json.dumps(summary,indent=2)+'\n')
    print(json.dumps(summary),flush=True)
    raise SystemExit(0 if summary['all_commands_succeeded'] else 1)

if __name__ == '__main__':
    main()
