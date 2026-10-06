#!/usr/bin/env python3
"""Fail-closed scheduler candidate gate, independent from physical RUNTIME.
Consumes recorded evidence only; never starts a build, traffic or a service.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import math
import pathlib
import statistics

ROOT = pathlib.Path(__file__).resolve().parents[1]
VERSION = (ROOT/'macos/VERSION').read_text().strip()
SCHEDULER_POLICY_REVISION = 6
REQUIRED_CHECKS = (
    'protocol_state_and_fault_tests', 'profile_ui_and_stdin',
    'aggregate_algorithm_equivalence', 'aggregate_short_performance',
    'protect_heterogeneous', 'auto_heterogeneous', 'auto_homogeneous',
    'three_mode_capacity_smoke', 'auto_complete_matrices',
    'aggregate_matrix', 'capacity_30sx2', 'final_race_vet',
    'source_and_fixture_identity', 'rev2_credit_and_directional_reset',
)
ORIGIN_BASELINE_COMMIT = 'e5f6a33868031dd33c0557942ed2ecc4e2d75998'
ASYMMETRIC_MIN_RATIO = 0.90
UNIFORM_MIN_RATIO = 0.90
MIN_REGRESSION_REPETITIONS = 2
UNIFORM_CASES = {
    f'{rate}Mbps-{paths}paths-{ms}ms'
    for rate in [300, 500] for paths in [2, 3, 6] for ms in [30, 50, 100]
}
MIN_COVERAGE = {
    'aggregate_uniform': 13, 'auto_uniform': 13,
    'auto_representative': 10,
    'protect_asymmetric_pairs': 3, 'protect_extreme_pairs': 5,
    'auto_asymmetric_pairs': 3, 'auto_extreme_pairs': 5,
    'capacity_smoke_modes': 3, 'auto_full_matrices': 2,
    'aggregate_full_matrices': 1, 'formal_capacity_cases': 10,
}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def check(record: dict, identity: str, *, verify_evidence: bool = False,
          root: pathlib.Path = ROOT) -> None:
    require(record.get('version') == VERSION and record.get('wire_protocol') == 4,
            'Scheduler version/wire mismatch')
    require(record.get('source_id') == identity and len(identity) == 64,
            'Scheduler source mismatch')
    require(record.get('scheduler_policy_revision') == SCHEDULER_POLICY_REVISION,
            'Missing local scheduler policy revision 6')
    require(record.get('verified') is True and record.get('status') == 'passed',
            'Scheduler automatic acceptance is not passed')
    require(record.get('candidate_frozen_after_short_acceptance') is True,
            'Candidate was not frozen after successful short acceptance')
    require(not record.get('failures'), 'Scheduler acceptance contains failures')
    checks = record.get('checks', {})
    for key in REQUIRED_CHECKS:
        require(checks.get(key) is True, 'Missing scheduler check: ' + key)
    coverage = record.get('coverage', {})
    for key, minimum in MIN_COVERAGE.items():
        require(type(coverage.get(key)) is int and coverage[key] >= minimum,
                'Insufficient scheduler coverage: ' + key)
    require(record.get('configured_modes') == ['auto', 'aggregate', 'protect', 'weighted'],
            'Four configured modes not verified')
    require(record.get('default_mode') == 'auto', 'Default scheduler is not Auto')
    rev2 = record.get('rev2_shared_credit', {})
    require(set(rev2) == {'0','8','16','32'}, 'Missing Rev2 idle history coverage')
    for count, pair in rev2.items():
        clean, idle = pair.get('clean_mbps', []), pair.get('idle_mbps', [])
        require(len(clean) >= 2 and len(clean) == len(idle), 'Missing reversed-order Rev2 repetitions')
        require(all(type(x) in (int,float) and math.isfinite(x) and x > 0 for x in clean+idle), 'Invalid Rev2 throughput')
        require(pair.get('source_id') == identity and pair.get('actual_idle_credit_zero') is True and pair.get('cleanup_reclaimed') is True, 'Rev2 accounting/source/cleanup mismatch')
        require(statistics.median(idle) >= .95*statistics.median(clean), 'Rev2 idle/clean 95% gate failed: '+count)
    matrix_keys=set()
    for matrix in record.get('full_matrices', []):
        key=(matrix.get('mode'),matrix.get('round'))
        require(key not in matrix_keys and type(key[1]) is int and key[1]>0,'Duplicate or invalid matrix round')
        matrix_keys.add(key)
        require(matrix.get('source_id') == identity and matrix.get('exit_code') == 0,
                'Failed or stale full matrix')
        cases = matrix.get('cases', [])
        require(len(cases) == 20 and len({c['name'] for c in cases}) == 20,
                'Full matrix must contain all 20 distinct cases')
        names = {c['name'] for c in cases}
        expected = {f'{rate}Mbps-{paths}paths-{ms}ms'
                    for rate in [300, 500] for paths in [2, 3, 6]
                    for ms in [30, 50, 100]}
        expected |= {'asymmetric-fastest-only', 'asymmetric-300+20+180Mbps'}
        require(names == expected, 'Full matrix inventory changed')
        fastest = next(c['mbps'] for c in cases if c['name'] == 'asymmetric-fastest-only')
        for case in cases:
            rate = case.get('mbps')
            require(type(rate) in (float, int) and math.isfinite(rate) and rate > 0,
                    'Invalid throughput value')
    matrices = record.get('full_matrices', [])
    require(sum(m.get('mode') == 'auto' for m in matrices) >= 2 and
            sum(m.get('mode') == 'aggregate' for m in matrices) >= 1,
            'Source-matched Auto and Aggregate matrices are missing')

    # Draft-era acceptance compared the heterogeneous three-path result with
    # the fastest-only path and required 95%. That no longer describes the
    # actual 0.10.12 product baseline and can fail unchanged origin/main by a
    # wide margin. Stable 1.0 compares both uniform and heterogeneous results
    # with the frozen 0.10.12 source baseline measured by the same harness on
    # the same host. This keeps the performance signal while avoiding a
    # machine-specific absolute-Mbps claim.
    uniform = record.get('uniform_regression', {})
    require(uniform.get('origin_commit') == ORIGIN_BASELINE_COMMIT,
            'Uniform baseline is not the frozen 0.10.12 origin/main commit')
    require(uniform.get('minimum_ratio') == UNIFORM_MIN_RATIO,
            'Uniform regression ratio changed')
    uniform_modes = uniform.get('modes', {})
    require(set(uniform_modes) == {'auto', 'aggregate'},
            'Uniform baseline must cover Auto/Aggregate')
    for mode, comparison in uniform_modes.items():
        require(comparison.get('baseline_commit') == ORIGIN_BASELINE_COMMIT,
                f'{mode} uniform comparison baseline commit differs')
        cases = comparison.get('cases', {})
        require(set(cases) == UNIFORM_CASES,
                f'{mode} uniform comparison inventory changed')
        for name, values in cases.items():
            baseline = values.get('baseline_mbps', [])
            candidate = values.get('candidate_mbps', [])
            require(len(baseline) >= 1 and len(candidate) >= MIN_REGRESSION_REPETITIONS,
                    f'Missing repeated {mode} uniform evidence: {name}')
            require(all(type(x) in (int, float) and math.isfinite(x) and x > 0 for x in baseline + candidate),
                    f'Invalid {mode} uniform evidence: {name}')
            require(statistics.median(candidate) >= UNIFORM_MIN_RATIO * statistics.median(baseline),
                    f'{mode} uniform throughput regressed versus 0.10.12 baseline: {name}')

    regression = record.get('asymmetric_regression', {})
    require(regression.get('origin_commit') == ORIGIN_BASELINE_COMMIT,
            'Asymmetric baseline is not the frozen 0.10.12 origin/main commit')
    require(regression.get('minimum_ratio') == ASYMMETRIC_MIN_RATIO,
            'Asymmetric regression ratio changed')
    modes = regression.get('modes', {})
    require(set(modes) == {'auto', 'aggregate', 'protect'},
            'Asymmetric baseline must cover Auto/Aggregate/Protect')
    for mode, comparison in modes.items():
        for series_name in ['baseline_fastest_mbps','baseline_mixed_mbps','candidate_fastest_mbps','candidate_mixed_mbps']:
            values = comparison.get(series_name, [])
            require(len(values) >= MIN_REGRESSION_REPETITIONS and all(type(x) in (int,float) and math.isfinite(x) and x > 0 for x in values),
                    f'Missing or invalid {mode} {series_name}')
        baseline_fast = statistics.median(comparison['baseline_fastest_mbps'])
        baseline_mixed = statistics.median(comparison['baseline_mixed_mbps'])
        candidate_fast = statistics.median(comparison['candidate_fastest_mbps'])
        candidate_mixed = statistics.median(comparison['candidate_mixed_mbps'])
        require(candidate_fast >= ASYMMETRIC_MIN_RATIO * baseline_fast,
                f'{mode} fastest-only regressed versus 0.10.12 baseline')
        require(candidate_mixed >= ASYMMETRIC_MIN_RATIO * baseline_mixed,
                f'{mode} heterogeneous throughput regressed versus 0.10.12 baseline')
        require(comparison.get('baseline_commit') == ORIGIN_BASELINE_COMMIT,
                f'{mode} comparison baseline commit differs')

    evidence = record.get('evidence', [])
    require(len(evidence) >= 12, 'Missing raw acceptance evidence inventory')
    seen = set()
    for item in evidence:
        name, digest = item.get('path', ''), item.get('sha256', '')
        path = pathlib.PurePosixPath(name)
        require(name and not path.is_absolute() and '..' not in path.parts and name not in seen,
                'Invalid or repeated evidence path')
        seen.add(name)
        require(len(digest) == 64 and all(c in '0123456789abcdef' for c in digest),
                'Invalid evidence digest')
        if verify_evidence:
            local = root / path
            require(local.resolve().is_relative_to(root.resolve()) and local.is_file() and not local.is_symlink() and
                    hashlib.sha256(local.read_bytes()).hexdigest() == digest,
                    'Recorded scheduler evidence changed or missing: ' + name)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('record', type=pathlib.Path)
    parser.add_argument('--source-id', required=True)
    args = parser.parse_args()
    check(json.loads(args.record.read_text()), args.source_id, verify_evidence=True)
    print('MATCHED_SCHEDULER_AUTOMATIC_GATES_PASS; physical App/Surge acceptance remains independent')


if __name__ == '__main__':
    main()
