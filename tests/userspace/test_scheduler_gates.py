#!/usr/bin/env python3
"""Synthetic gate tests. These never emit real acceptance or run traffic."""
from pathlib import Path
import copy
import hashlib
import importlib.util
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('scheduler_gates', ROOT/'scripts/scheduler-gates.py')
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
IDENTITY = 'a'*64


def fixture():
    cases = [{'name': f'{rate}Mbps-{paths}paths-{ms}ms', 'mbps': 270.0 if rate == 300 else 390.0}
             for rate in [300, 500] for paths in [2, 3, 6] for ms in [30, 50, 100]]
    cases += [{'name': 'asymmetric-fastest-only', 'mbps': 270.0},
              {'name': 'asymmetric-300+20+180Mbps', 'mbps': 380.0}]
    return {'version': '0.9.4', 'wire_protocol': 3, 'source_id': IDENTITY,
            'scheduler_capability_revision': 5, 'verified': True, 'status': 'passed',
            'candidate_frozen_after_short_acceptance': True, 'failures': [],
            'checks': dict.fromkeys(gate.REQUIRED_CHECKS, True),
            'coverage': dict(gate.MIN_COVERAGE), 'default_mode': 'auto',
            'configured_modes': ['auto', 'aggregate', 'protect', 'weighted'],
            'rev2_shared_credit': {str(n): {'source_id': IDENTITY, 'clean_mbps':[150,152], 'idle_mbps':[149,153], 'actual_idle_credit_zero':True, 'cleanup_reclaimed':True} for n in [0,8,16,32]},
            'full_matrices': [{'mode': mode, 'round': n, 'source_id': IDENTITY, 'exit_code': 0,
                               'cases': copy.deepcopy(cases)}
                              for mode, n in [('auto', 1), ('auto', 2), ('aggregate', 1)]],
            'evidence': [{'path': f'reports/test/evidence-{i}.json', 'sha256': 'b'*64} for i in range(12)]}


class SchedulerGateTests(unittest.TestCase):
    def test_complete_synthetic_record(self):
        gate.check(fixture(), IDENTITY)

    def test_each_required_check_is_mandatory(self):
        for key in gate.REQUIRED_CHECKS:
            with self.subTest(key=key):
                data = fixture()
                data['checks'][key] = False
                with self.assertRaises(ValueError):
                    gate.check(data, IDENTITY)

    def test_each_coverage_minimum_is_mandatory(self):
        for key in gate.MIN_COVERAGE:
            with self.subTest(key=key):
                data = fixture()
                data['coverage'][key] -= 1
                with self.assertRaises(ValueError):
                    gate.check(data, IDENTITY)

    def test_identity_capability_and_candidate_state(self):
        for key, value in [('source_id', 'c'*64), ('wire_protocol', 2),
                           ('scheduler_capability_revision', 0), ('verified', False),
                           ('candidate_frozen_after_short_acceptance', False),
                           ('status', 'pending'), ('failures', ['failed extreme case'])]:
            with self.subTest(key=key):
                data = fixture()
                data[key] = value
                with self.assertRaises(ValueError):
                    gate.check(data, IDENTITY)

    def test_rev2_credit_gate_cannot_be_omitted_or_underperform(self):
        for key in ['actual_idle_credit_zero', 'cleanup_reclaimed']:
            data = fixture()
            data['rev2_shared_credit']['16'][key] = False
            with self.assertRaises(ValueError):
                gate.check(data, IDENTITY)
        data=fixture()
        data['rev2_shared_credit']['32']['idle_mbps']=[120,121]
        with self.assertRaises(ValueError):
            gate.check(data, IDENTITY)

    def test_full_matrix_thresholds_not_weakened(self):
        for name, value in [('300Mbps-6paths-50ms', 239.99),
                            ('500Mbps-6paths-30ms', 299.99),
                            ('asymmetric-300+20+180Mbps', 256.0)]:
            data = fixture()
            next(c for c in data['full_matrices'][0]['cases'] if c['name'] == name)['mbps'] = value
            with self.assertRaises(ValueError):
                gate.check(data, IDENTITY)

    def test_matrix_inventory_and_source_are_required(self):
        data = fixture()
        data['full_matrices'][1]['cases'].pop()
        with self.assertRaises(ValueError):
            gate.check(data, IDENTITY)
        data = fixture()
        data['full_matrices'][0]['source_id'] = 'd'*64
        with self.assertRaises(ValueError):
            gate.check(data, IDENTITY)
        data = fixture()
        data['full_matrices'][0]['exit_code'] = 1
        with self.assertRaises(ValueError):
            gate.check(data, IDENTITY)

    def test_evidence_hash_is_checked(self):
        data = fixture()
        with tempfile.TemporaryDirectory(prefix='scheduler-gate-test-') as directory:
            root = Path(directory)
            for index, item in enumerate(data['evidence']):
                path = root/item['path']
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('synthetic gate test '+str(index))
                item['sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
            gate.check(data, IDENTITY, verify_evidence=True, root=root)
            (root/data['evidence'][0]['path']).write_text('modified')
            with self.assertRaises(ValueError):
                gate.check(data, IDENTITY, verify_evidence=True, root=root)


if __name__ == '__main__':
    unittest.main()
