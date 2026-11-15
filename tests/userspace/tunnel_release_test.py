#!/usr/bin/env python3
"""Packaging contract tests use synthetic bytes; never claim platform execution."""
import copy
import importlib.util
import pathlib
import tarfile
import tempfile
import unittest
from unittest.mock import patch
ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('tunnel_suite', ROOT/'scripts/tunnel-suite.py')
suite = importlib.util.module_from_spec(spec)
spec.loader.exec_module(suite)


class TunnelReleaseGateTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = pathlib.Path(tmp.name)
        self.identity = 'a'*64
        self.version = '1.2.1'
        (self.root/'pass.log').write_bytes(b'synthetic test evidence\n')
        commands = [{'name': name, 'source_id': self.identity, 'status': 'passed', 'exit_code': 0,
                     'log': 'pass.log', 'sha256': suite.source.sha((self.root/'pass.log').read_bytes())}
                    for name in sorted(suite.REQUIRED_GATES)]
        self.tests = {'version': self.version, 'source_id': self.identity, 'verified': True,
                      'commands': commands, 'native_ci': {p: {'conclusion': 'success', 'run_id': i,
                      'head_sha': 'b'*40, 'source_id': self.identity} for i, p in enumerate(('linux', 'windows'), 1)}}
        hashes = {}
        for name in suite.artifact_names(self.version):
            (self.root/name).write_bytes(('synthetic '+name).encode())
            hashes[name] = suite.source.sha((self.root/name).read_bytes())
        for name in suite.NATIVE_BINARIES:
            (self.root/(name+'.sha256')).write_text(hashes[name]+'  '+name+'\n')
        self.provenance = {'version': self.version, 'source_id': self.identity, 'verified': True,
                           'update_signatures_verified': True, 'artifact_sha256': hashes}

    def test_exact_matching_suite_passes(self):
        suite.check_tests(self.tests, self.root, self.version, self.identity)
        suite.check_provenance(self.provenance, self.root, self.version, self.identity)

    def test_every_gate_is_required(self):
        for name in suite.REQUIRED_GATES:
            with self.subTest(name=name):
                doc = copy.deepcopy(self.tests)
                doc['commands'] = [r for r in doc['commands'] if r['name'] != name]
                with self.assertRaises(ValueError): suite.check_tests(doc, self.root, self.version, self.identity)

    def test_skipped_failed_old_and_duplicate_gates_rejected(self):
        for field, value in [('exit_code', 1), ('status', 'skipped'), ('source_id', 'old'), ('sha256', '0'*64)]:
            with self.subTest(field=field):
                doc = copy.deepcopy(self.tests); doc['commands'][0][field] = value
                with self.assertRaises(ValueError): suite.check_tests(doc, self.root, self.version, self.identity)
        doc = copy.deepcopy(self.tests); doc['commands'].append(doc['commands'][0])
        with self.assertRaises(ValueError): suite.check_tests(doc, self.root, self.version, self.identity)

    def test_native_ci_must_be_current_success_and_same_commit(self):
        for field, value in [('conclusion', 'failure'), ('conclusion', 'skipped'), ('source_id', 'old'), ('head_sha', 'c'*40), ('run_id', 0)]:
            doc = copy.deepcopy(self.tests); doc['native_ci']['windows'][field] = value
            with self.assertRaises(ValueError): suite.check_tests(doc, self.root, self.version, self.identity)

    def test_changed_evidence_rejected(self):
        (self.root/'pass.log').write_bytes(b'different evidence')
        with self.assertRaises(ValueError): suite.check_tests(self.tests, self.root, self.version, self.identity)

    def test_missing_windows_assets_rejected(self):
        for name in ('windows-update.json', 'MPTCP-Desk-1.2.1-Windows-Setup.exe', 'MPTCP-Desk-1.2.1-Windows-Portable.zip'):
            doc = copy.deepcopy(self.provenance); del doc['artifact_sha256'][name]
            with self.assertRaises(ValueError): suite.check_provenance(doc, self.root, self.version, self.identity)

    def test_build_only_provenance_cannot_promote(self):
        doc = copy.deepcopy(self.provenance); doc['update_signatures_verified'] = False
        with self.assertRaises(ValueError): suite.check_provenance(doc, self.root, self.version, self.identity)

    def test_stale_or_modified_binary_rejected(self):
        doc = copy.deepcopy(self.provenance); doc['source_id'] = 'old'
        with self.assertRaises(ValueError): suite.check_provenance(doc, self.root, self.version, self.identity)
        (self.root/'mptcp-landing').write_bytes(b'changed')
        with self.assertRaises(ValueError): suite.check_provenance(self.provenance, self.root, self.version, self.identity)

    def test_absolute_checksum_path_rejected(self):
        name = 'mptcp-landing'
        (self.root/(name+'.sha256')).write_text(self.provenance['artifact_sha256'][name]+'  /private/build/'+name+'\n')
        with self.assertRaises(ValueError): suite.check_provenance(self.provenance, self.root, self.version, self.identity)

    def test_unsafe_paths_and_symlinks_rejected(self):
        for name in ('../outside', '/tmp/outside', 'C:\\outside', ''):
            with self.assertRaises(ValueError): suite.safe_file(self.root, name)
        (self.root/'link').symlink_to(self.root/'pass.log')
        with self.assertRaises(ValueError): suite.safe_file(self.root, 'link')

    def test_native_executables_keep_permissions(self):
        files = {name: b'binary' for name in suite.NATIVE_BINARIES}
        files['README.md'] = b'doc'
        archive = self.root/'suite.tar.gz'
        suite.source.archive(archive, files)
        with tarfile.open(archive) as t:
            for name in suite.NATIVE_BINARIES: self.assertEqual(t.getmember(name).mode, 0o755)
            self.assertEqual(t.getmember('README.md').mode, 0o644)

    def test_homebrew_license_layout(self):
        root = self.root/'libexec'; root.mkdir()
        (self.root/'LICENSE').write_bytes(b'upstream license')
        with patch.object(suite.subprocess, 'check_output', return_value=str(root)+'\n'):
            self.assertEqual(suite.go_license('go'), b'upstream license')
        (self.root/'LICENSE').unlink()
        with patch.object(suite.subprocess, 'check_output', return_value=str(root)+'\n'):
            with self.assertRaises(ValueError): suite.go_license('go')

    def test_verifier_includes_tunnel_swift_and_new_schema(self):
        text = (ROOT/'scripts/verify-userspace.py').read_text()
        for name in ('TunnelProfile.swift', 'TunnelView.swift', 'tunnel.example.json', 'validate-tunnel'):
            self.assertIn(name, text)
        self.assertNotIn("files['macos/userspace-profile.example.json']", text)


if __name__ == '__main__': unittest.main()
