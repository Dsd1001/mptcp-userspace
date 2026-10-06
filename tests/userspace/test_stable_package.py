from __future__ import annotations
import copy
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('stable_package', ROOT/'scripts/package-userspace.py')
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)
IDENTITY = 'a'*64


def scheduler_fixture(root: pathlib.Path) -> dict:
    cases = [{'name': f'{rate}Mbps-{paths}paths-{ms}ms', 'mbps': 270.0 if rate == 300 else 390.0}
             for rate in [300, 500] for paths in [2, 3, 6] for ms in [30, 50, 100]]
    cases += [{'name': 'asymmetric-fastest-only', 'mbps': 270.0},
              {'name': 'asymmetric-300+20+180Mbps', 'mbps': 380.0}]
    evidence = []
    for i in range(12):
        rel = pathlib.Path('evidence')/f'scheduler-{i}.json'
        path = root/rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('synthetic scheduler evidence '+str(i))
        evidence.append({'path': rel.as_posix(), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
    return {
        'version': package.STABLE_VERSION,
        'wire_protocol': 4,
        'source_id': IDENTITY,
        'scheduler_policy_revision': package.scheduler_gates.SCHEDULER_POLICY_REVISION,
        'verified': True,
        'status': 'passed',
        'candidate_frozen_after_short_acceptance': True,
        'failures': [],
        'checks': dict.fromkeys(package.scheduler_gates.REQUIRED_CHECKS, True),
        'coverage': dict(package.scheduler_gates.MIN_COVERAGE),
        'default_mode': 'auto',
        'configured_modes': ['auto', 'aggregate', 'protect', 'weighted'],
        'rev2_shared_credit': {
            str(n): {
                'source_id': IDENTITY,
                'clean_mbps': [150, 152],
                'idle_mbps': [149, 153],
                'actual_idle_credit_zero': True,
                'cleanup_reclaimed': True,
            } for n in [0, 8, 16, 32]
        },
        'full_matrices': [
            {'mode': mode, 'round': n, 'source_id': IDENTITY, 'exit_code': 0, 'cases': copy.deepcopy(cases)}
            for mode, n in [('auto', 1), ('auto', 2), ('aggregate', 1)]
        ],
        'uniform_regression': {
            'origin_commit': package.scheduler_gates.ORIGIN_BASELINE_COMMIT,
            'minimum_ratio': package.scheduler_gates.UNIFORM_MIN_RATIO,
            'modes': {mode: {
                'baseline_commit': package.scheduler_gates.ORIGIN_BASELINE_COMMIT,
                'cases': {c['name']: {'baseline_mbps': [270.0], 'candidate_mbps': [271.0, 272.0]}
                          for c in cases if 'asymmetric' not in c['name']},
            } for mode in ['auto', 'aggregate']},
        },
        'asymmetric_regression': {
            'origin_commit': package.scheduler_gates.ORIGIN_BASELINE_COMMIT,
            'minimum_ratio': package.scheduler_gates.ASYMMETRIC_MIN_RATIO,
            'modes': {mode: {
                'baseline_commit': package.scheduler_gates.ORIGIN_BASELINE_COMMIT,
                'baseline_fastest_mbps': [270.0, 272.0, 268.0],
                'baseline_mixed_mbps': [110.0, 112.0, 108.0],
                'candidate_fastest_mbps': [271.0, 273.0, 269.0],
                'candidate_mixed_mbps': [111.0, 113.0, 109.0],
            } for mode in ['auto','aggregate','protect']}
        },
        'evidence': evidence,
    }


class StablePackageTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)
        commands = []
        for name in sorted(package.STABLE_REQUIRED):
            rel = pathlib.Path('logs')/(name+'.log')
            path = self.root/rel
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('passed\n')
            commands.append({
                'name': name,
                'exit_code': 0,
                'log': rel.as_posix(),
                'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
            })
        self.tests = {
            'version': package.STABLE_VERSION,
            'source_id': IDENTITY,
            'verified': True,
            'status': 'correctness-passed',
            'commands': commands,
        }
        self.scheduler = scheduler_fixture(self.root)

    def check(self):
        package.check_stable_release(
            self.tests, self.scheduler, IDENTITY, package.STABLE_VERSION, root=self.root)

    def test_complete_stable_shape_passes(self):
        self.check()

    def test_missing_stable_command_rejected(self):
        self.tests['commands'] = [
            r for r in self.tests['commands'] if r['name'] != 'mpx4-stable-core'
        ]
        with self.assertRaises(ValueError):
            self.check()

    def test_scheduler_is_local_policy_revision_6(self):
        self.scheduler['scheduler_policy_revision'] = 5
        with self.assertRaises(ValueError):
            self.check()

    def test_changed_evidence_rejected(self):
        item = self.tests['commands'][0]
        (self.root/item['log']).write_text('changed\n')
        with self.assertRaises(ValueError):
            self.check()

    def test_acceptance_text_names_stable_boundary(self):
        text = package.acceptance_text(
            IDENTITY, {'verified': False}, {'verified': False}, False, self.scheduler,
            version=package.STABLE_VERSION, stable_release=True)
        self.assertIn('MPX/4 Protocol Version 4 Stable', text)
        self.assertIn(package.STABLE_PROTOCOL_RELEASE, text)
        self.assertIn(package.STABLE_BROKER_SHA256, text)
        self.assertNotIn('scheduler capability revision 5', text)


if __name__ == '__main__':
    unittest.main()
