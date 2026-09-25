from __future__ import annotations
import copy
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest

ROOT=pathlib.Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('preview_package',ROOT/'scripts/package-userspace.py')
package=importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)
ID='a'*64
VERSION='0.9.4'

class PreviewPackageTests(unittest.TestCase):
    def setUp(self):
        self.directory=tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root=pathlib.Path(self.directory.name)
        self.tests={'version':VERSION,'source_id':ID,'verified':True,'status':'correctness-passed','commands':[]}
        for name in ('go-test','go-vet','go-race','warm-seed-race','release-script-tests'):
            path=self.root/(name+'.log');path.write_text('passed\n')
            self.tests['commands'].append({'name':name,'exit_code':0,'log':path.name,'sha256':hashlib.sha256(path.read_bytes()).hexdigest()})
        self.scheduler={'version':VERSION,'source_id':ID,'wire_protocol':3,'scheduler_capability_revision':5,'verified':False,'status':'preview-with-known-limitations','known_limitations':['small-request p99 and duplex targets not all met']}
    def check(self):
        package.check_preview(self.tests,self.scheduler,ID,VERSION,root=self.root)
    def test_preview_retains_known_limitations(self):
        self.check()
        text=package.acceptance_text(ID,{'verified':False},{'verified':False},False,self.scheduler,version=VERSION,preview=True)
        self.assertIn('preview-with-known-limitations',text)
        self.assertNotIn('untested-by-request release',text)
        self.assertIn('Small-request p99 regressed',text)
    def test_failed_test_rejected(self):
        self.tests['commands'][0]['exit_code']=1
        with self.assertRaises(ValueError):self.check()
    def test_changed_log_rejected(self):
        (self.root/'go-test.log').write_text('tampered\n')
        with self.assertRaises(ValueError):self.check()
    def test_missing_or_duplicate_check_rejected(self):
        self.tests['commands'][-1]=copy.deepcopy(self.tests['commands'][0])
        with self.assertRaises(ValueError):self.check()
    def test_stale_identity_rejected(self):
        self.tests['source_id']='b'*64
        with self.assertRaises(ValueError):self.check()
    def test_performance_promotion_not_implied(self):
        self.scheduler['verified']=True
        with self.assertRaises(ValueError):self.check()
    def test_hidden_limitations_rejected(self):
        self.scheduler['known_limitations']=[]
        with self.assertRaises(ValueError):self.check()
    def test_unsafe_log_path_rejected(self):
        self.tests['commands'][0]['log']='../outside.log'
        with self.assertRaises(ValueError):self.check()

if __name__=='__main__':unittest.main()
