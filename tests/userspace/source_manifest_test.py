#!/usr/bin/env python3
import importlib.util
import io
import pathlib
import tarfile
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("source_manifest", ROOT / "scripts" / "source-manifest.py")
MOD = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MOD)


class SourceManifestCanonicalizationTest(unittest.TestCase):
    def test_text_newlines_are_checkout_independent(self):
        path = pathlib.Path("example.go")
        lf = b"package p\n\nfunc f() {}\n"
        crlf = lf.replace(b"\n", b"\r\n")
        cr = lf.replace(b"\n", b"\r")
        self.assertEqual(MOD.canonical_source_bytes(path, lf), lf)
        self.assertEqual(MOD.canonical_source_bytes(path, crlf), lf)
        self.assertEqual(MOD.canonical_source_bytes(path, cr), lf)
        self.assertEqual(MOD.sha(MOD.canonical_source_bytes(path, lf)), MOD.sha(MOD.canonical_source_bytes(path, crlf)))

    def test_binary_assets_are_exact(self):
        path = pathlib.Path("icon.png")
        data = b"\x89PNG\r\n\x1a\nraw\r\nbytes"
        self.assertEqual(MOD.canonical_source_bytes(path, data), data)


class FrozenSourceGateTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        (self.root/'macos').mkdir()
        (self.root/'macos/VERSION').write_text('1.2.0\n')
        self.files = {'macos/VERSION': b'1.2.0\n', 'example.go': b'package p\n'}
        self.sums = MOD.manifest(self.files)
        self.identity = MOD.sha(self.sums)
        _, self.out, self.name = MOD.release_layout(self.root)
        self.out.mkdir(parents=True)
        generated = {'SOURCE_ID': (self.identity+'\n').encode(), 'SOURCE_SHA256SUMS': self.sums}
        for folder in (self.root, self.out):
            for relative, content in generated.items():
                MOD.atomic_bytes(folder/relative, content)
        self.archived = dict(self.files, **generated)
        MOD.archive(self.out/self.name, self.archived)

    def test_matching_root_package_and_archive_pass(self):
        self.assertEqual(MOD.check_frozen(self.root, self.files), self.identity)

    def test_each_manifest_copy_is_verified(self):
        for folder in (self.root, self.out):
            for name in ('SOURCE_ID', 'SOURCE_SHA256SUMS'):
                with self.subTest(folder=folder.name, manifest=name):
                    path = folder/name
                    original = path.read_bytes()
                    path.write_bytes(b'stale\n')
                    with self.assertRaisesRegex(ValueError, 'Frozen source drift'):
                        MOD.check_frozen(self.root, self.files)
                    path.write_bytes(original)

    def test_changed_source_is_not_silently_refrozen(self):
        changed = dict(self.files, **{'example.go': b'package changed\n'})
        with self.assertRaisesRegex(ValueError, 'Frozen source drift'):
            MOD.check_frozen(self.root, changed)
        self.assertEqual((self.root/'SOURCE_ID').read_text().strip(), self.identity)

    def test_crlf_checkout_manifests_are_equivalent(self):
        for folder in (self.root, self.out):
            for name in ('SOURCE_ID', 'SOURCE_SHA256SUMS'):
                path = folder/name
                path.write_bytes(path.read_bytes().replace(b'\n', b'\r\n'))
        self.assertEqual(MOD.check_frozen(self.root, self.files), self.identity)

    def test_missing_manifest_fails(self):
        (self.root/'SOURCE_ID').unlink()
        with self.assertRaisesRegex(ValueError, 'Missing or linked'):
            MOD.check_frozen(self.root, self.files)

    def test_mismatching_archive_bytes_fail(self):
        changed = dict(self.archived, **{'example.go': b'package changed\n'})
        MOD.archive(self.out/self.name, changed)
        with self.assertRaisesRegex(ValueError, 'archive content differs'):
            MOD.check_frozen(self.root, self.files)

    def test_duplicate_archive_members_fail(self):
        with tarfile.open(self.out/self.name, 'w:gz') as archive:
            for name, data in list(self.archived.items()) + [('example.go', self.files['example.go'])]:
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                archive.addfile(entry, io.BytesIO(data))
        with self.assertRaisesRegex(ValueError, 'archive inventory differs'):
            MOD.check_frozen(self.root, self.files)

    def test_extra_archive_members_fail(self):
        MOD.archive(self.out/self.name, dict(self.archived, extra=b'unreviewed'))
        with self.assertRaisesRegex(ValueError, 'archive inventory differs'):
            MOD.check_frozen(self.root, self.files)


if __name__ == "__main__":
    unittest.main()
