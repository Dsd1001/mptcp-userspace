#!/usr/bin/env python3
import importlib.util
import pathlib
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


if __name__ == "__main__":
    unittest.main()
