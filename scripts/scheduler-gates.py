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
REQUIRED_CHECKS = (
    'protocol_state_and_fault_tests', 'profile_ui_and_stdin',
    'aggregate_algorithm_equivalence', 'aggregate_short_performance',
    'protect_heterogeneous', 'auto_heterogeneous', 'auto_homogeneous',
    'three_mode_capacity_smoke', 'auto_complete_matrices',
    'aggregate_matrix', 'capacity_30sx2', 'final_race_vet',
    'source_and_fixture_identity', 'rev2_credit_and_directional_reset',
)
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
    require(record.get('version') == '0.9.4' and record.get('wire_protocol') == 3,
            'Scheduler version/wire mismatch')
    require(record.get('source_id') == identity and len(identity) == 64,
            'Scheduler source mismatch')
    require(record.get('scheduler_capability_revision') == 5,
            'Missing authenticated scheduler capability revision 5')
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
            name = case['name']
            threshold = None
            if name.startswith('300Mbps-') and not name.endswith('-100ms'):
                threshold = 240
            if name.startswith('500Mbps-') and not name.endswith('-100ms'):
                threshold = 300
            if name == 'asymmetric-300+20+180Mbps':
                threshold = .95 * fastest
            require(threshold is None or rate >= threshold,
                    'Original full-matrix performance gate failed: ' + name)
    matrices = record.get('full_matrices', [])
    require(sum(m.get('mode') == 'auto' for m in matrices) >= 2 and
            sum(m.get('mode') == 'aggregate' for m in matrices) >= 1,
            'Source-matched Auto and Aggregate matrices are missing')
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
