from __future__ import annotations
import copy
import hashlib
import importlib.util
import pathlib
import tempfile
import unittest

ROOT=pathlib.Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('background_package',ROOT/'scripts/package-userspace.py')
package=importlib.util.module_from_spec(spec);spec.loader.exec_module(package)
ID='a'*64
VERSION='0.9.5'

class BackgroundPackageTests(unittest.TestCase):
    def setUp(self):
        self.directory=tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root=pathlib.Path(self.directory.name)
        self.tests={'version':VERSION,'source_id':ID,'verified':True,'status':'correctness-passed','commands':[]}
        for name in sorted(package.BACKGROUND_REQUIRED):
            path=self.root/(name+'.log');path.write_text('passed\n')
            self.tests['commands'].append({'name':name,'exit_code':0,'log':path.name,'sha256':hashlib.sha256(path.read_bytes()).hexdigest()})
        self.scheduler={
            'version':VERSION,'source_id':ID,'wire_protocol':3,'scheduler_capability_revision':5,
            'verified':True,'status':'background-release-passed','default_mode':'auto',
            'configured_modes':['auto','aggregate','protect','weighted'],
            'checks':{
                'directional_authenticated_capacity':True,'upload_blank_auto':True,
                'penalty_timeout_protection':True,'legacy_modes_regression':True,
                'weighted_highbdp':True,'background_resident_policy':True,
            }
        }

    def check(self):
        package.check_background_release(self.tests,self.scheduler,ID,VERSION,root=self.root)

    def test_complete_record_passes(self):
        self.check()

    def test_missing_or_duplicate_test_rejected(self):
        self.tests['commands'].pop()
        with self.assertRaises(ValueError): self.check()
        self.setUp()
        self.tests['commands'].append(copy.deepcopy(self.tests['commands'][0]))
        with self.assertRaises(ValueError): self.check()

    def test_failed_changed_or_unsafe_log_rejected(self):
        self.tests['commands'][0]['exit_code']=1
        with self.assertRaises(ValueError): self.check()
        self.setUp()
        (self.root/self.tests['commands'][0]['log']).write_text('changed\n')
        with self.assertRaises(ValueError): self.check()
        self.setUp()
        self.tests['commands'][0]['log']='../outside.log'
        with self.assertRaises(ValueError): self.check()

    def test_stale_identity_or_scheduler_rejected(self):
        self.tests['source_id']='b'*64
        with self.assertRaises(ValueError): self.check()
        self.setUp()
        self.scheduler['scheduler_capability_revision']=4
        with self.assertRaises(ValueError): self.check()

    def test_each_background_check_is_required(self):
        for key in list(self.scheduler['checks']):
            with self.subTest(key=key):
                tests=copy.deepcopy(self.tests);scheduler=copy.deepcopy(self.scheduler)
                scheduler['checks'][key]=False
                with self.assertRaises(ValueError):
                    package.check_background_release(tests,scheduler,ID,VERSION,root=self.root)

if __name__=='__main__': unittest.main()
