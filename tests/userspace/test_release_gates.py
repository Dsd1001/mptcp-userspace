#!/usr/bin/env python3
"""Synthetic unit fixtures only: never produce an actual acceptance receipt."""
import copy, importlib.util, pathlib, unittest
ROOT=pathlib.Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('gates',ROOT/'scripts/release-gates.py')
G=importlib.util.module_from_spec(spec);spec.loader.exec_module(G)
ID='synthetic-unit-test-not-a-source-freeze'


def records():
    capacity={'version':'0.9.4','wire_protocol':3,'source_id':ID,'verified':True,'mode':'actual-logical-streams-30sx2','cases':[]}
    for n in [128,256,512,1024,2048]:
        for round_number in [1,2]:
            capacity['cases'].append({'target_streams':n,'round':round_number,'seconds':30.1,'passed':True,
                'client_peak':{'active_streams':n},'server_peak':{'active_streams':n},'open_receive_credit_waits':0,
                'admission_deadline_exceeded':0,'unexpected_resource_refusals':0,'six_carriers_preserved':True,
                'same_session':True,'reclaimed':True})
    for d in capacity['cases']:
        for who in ['client_peak','server_peak']:
            for k in ['receive_credit_bytes','bootstrap_credit_bytes','growth_credit_bytes','receive_allocated_bytes','data_pending_frames','data_pending_bytes','control_pending_frames','control_pending_bytes']:d[who][k]=0
            d[who]['capability_revision']=5
        d.update(exchanges=d['target_streams'],churn_reopens=8,bulk_segments=8,typed_next_stream_rejection=d['target_streams']==2048,evidence_sha256='0'*64)
    runtime={'version':'0.9.4','wire_protocol':3,'source_id':ID,'verified':True,'workload':'segmented-mixed-180s',
             'observed_seconds':180.1,'short_attempts':200,'short_failures':0,'idle_keepalive_connections':6,
             'idle_integrity':True,'minimum_sampled_paths':6,'final_paths':6,'open_receive_credit_waits':0,
             'admission_deadline_exceeded':0,'resource_refusals':0,'bulk_segments':[]}
    for k in ['same_session','no_carrier_reconnect','body_integrity','origin_body_hash_matched','surge_policy_verified',
              'installed_app_child_verified','protected_services_unchanged','protected_files_unchanged','fixture_stopped','temporary_route_removed']:
        runtime[k]=True
    for i in range(8):runtime['bulk_segments'].append({'success':True,'start_seconds':8+i*20,'end_seconds':15+i*20,'bytes':123456})
    return capacity,runtime


class GateTests(unittest.TestCase):
    def test_positive_synthetic_shape(self):
        self.assertTrue(G.check(*records(),ID,required=True))
    def test_pending_is_not_release(self):
        c,r=records();r['verified']=False
        self.assertFalse(G.check(c,r,ID,required=False))
        with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
    def test_old_wire_and_source_refused(self):
        for field,value in [('version','0.7.1'),('wire_protocol',2),('source_id','old')]:
            c,r=records();r[field]=value
            with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
    def test_missing_or_duplicate_capacity_refused(self):
        for mode in ['missing','duplicate','http-count','five-seconds']:
            c,r=records()
            if mode=='missing':c['cases'].pop()
            elif mode=='duplicate':c['cases'][-1]=copy.deepcopy(c['cases'][0])
            elif mode=='http-count':c['cases'][-1]['client_peak']['active_streams']=9
            else:c['cases'][-1]['seconds']=5
            with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
    def test_long_download_does_not_replace_mixed(self):
        for mode in ['old-long','one-bulk','no-idle','whole-run-bulk']:
            c,r=records()
            if mode=='old-long':r['observed_seconds']=1810
            elif mode=='one-bulk':r['bulk_segments']=r['bulk_segments'][:1]
            elif mode=='no-idle':r['idle_keepalive_connections']=0
            else:r['bulk_segments'][0]['start_seconds']=0;r['bulk_segments'][0]['end_seconds']=180
            with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
    def test_overbound_capacity_peak_refused(self):
        c,r=records();c['cases'][-1]['server_peak']['growth_credit_bytes']=97<<20
        with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
    def test_failure_or_missing_cleanup_refused(self):
        for field in ['resource_refusals','admission_deadline_exceeded','open_receive_credit_waits','short_failures']:
            c,r=records();r[field]=1
            with self.assertRaises(ValueError):G.check(c,r,ID,required=True)
        for field in ['fixture_stopped','installed_app_child_verified','origin_body_hash_matched','same_session']:
            c,r=records();r[field]=False
            with self.assertRaises(ValueError):G.check(c,r,ID,required=True)

if __name__=='__main__':unittest.main()
