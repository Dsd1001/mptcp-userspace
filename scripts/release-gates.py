#!/usr/bin/env python3
"""Bind 0.9.4 to ten actual 30s capacity cases and a 180s physical mixed run.
Only reads recorded evidence; never starts traffic, changes a service or GUI.
"""
from __future__ import annotations
import argparse, hashlib, importlib.util, json, pathlib
ROOT=pathlib.Path(__file__).resolve().parents[1]
VERSION='0.9.4'
TARGETS=[128,256,512,1024,2048]
MAX_STREAMS=2048
CAPS={'active_streams':MAX_STREAMS,'receive_credit_bytes':128<<20,'bootstrap_credit_bytes':32<<20,
      'growth_credit_bytes':96<<20,'receive_allocated_bytes':128<<20,'data_pending_frames':8192,
      'data_pending_bytes':128<<20,'control_pending_frames':8192,'control_pending_bytes':512<<10}


def require(condition: bool, message: str) -> None:
    if not condition: raise ValueError(message)


def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def bounds(snapshot: dict) -> None:
    r=snapshot['resources']
    require(r.get('capability_revision')==5,'Capacity evidence is not MPX/3 Rev5')
    for field,limit in CAPS.items():
        require(isinstance(r[field],int) and 0<=r[field]<=limit,'Invalid resource bound: '+field)
    require(r['receive_credit_bytes']==r['bootstrap_credit_bytes']+r['growth_credit_bytes'],'Credit subledger mismatch')
    require(r['pending_frames']==r['data_pending_frames']+r['control_pending_frames'],'Frame subledger mismatch')
    require(r['pending_bytes']==r['data_pending_bytes']+r['control_pending_bytes'],'Byte subledger mismatch')
    require(r['open_receive_credit_waits']==0 and r['waits'].get('receive_credit',0)==0,'OPEN waited for receive credit')


def reclaimed(snapshot: dict) -> bool:
    return all(snapshot.get(k,0)==0 for k in ['connections','pending_bytes','ready_frames','receive_credit_bytes','receive_allocated_bytes','buffered_bytes']) and all(snapshot['resources'].get(k,0)==0 for k in ['growth_credit_bytes','data_pending_frames','control_pending_frames','window_blocked_writers','waiting_opens','closing_streams','session_tx_unconsumed_bytes','session_tx_growth_bytes'])


def collect(directory: pathlib.Path, identity: str) -> dict:
    cases=[]
    for target in TARGETS:
        for round_number in [1,2]:
            path=directory/f'capacity-{target}-{round_number}.json'
            d=json.loads(path.read_text())
            require(d['version']==VERSION and d['source_id']==identity and d['wire_protocol']==3,'Capacity source/version mismatch')
            require(d['target_streams']==target and d['round']==round_number,'Capacity case mismatch')
            require(d['passed'] is True and d['acceptance_eligible'] is True and not d['failures'],'Failed or ineligible capacity case')
            require(d['requested_seconds']==30 and 30<=d['observed_seconds']<=35,'Not a bounded 30-second case')
            samples=d['samples'];require(len(samples)>=250 and samples[0]['seconds']==0 and samples[-1]['seconds']>=29.9,'Missing capacity timeline')
            peaks={who:{} for who in ['client','server']}
            for who in ['client','server']:
                initial=samples[0][who];tag=initial['lifecycle']['session_tag']
                require(initial['connections']==target and initial['resources']['active_streams']==target,'Actual concurrency target never reached')
                paths={p['id']:p for p in initial['path_stats']}
                require(len(paths)==6,'Six real carriers missing')
                for row in samples:
                    snapshot=row[who];bounds(snapshot);r=snapshot['resources']
                    require(target-5<=snapshot['connections']<=target,'Capacity occupancy dropped outside intended five churn slots')
                    require(snapshot['paths']==6 and snapshot['lifecycle']['session_tag']==tag and not snapshot['lifecycle']['closed'],'Shared transport changed')
                    refusals={k:v for k,v in r['rejections'].items() if v}
                    allowed=who=='client' and target==MAX_STREAMS and refusals=={'streams':1}
                    require(not refusals or allowed,'Unexpected capacity refusal')
                    for p in snapshot['path_stats']:
                        before=paths[p['id']]
                        require(p['connected'] and p['carrier_connections']==before['carrier_connections'] and p['dial_attempts']==before['dial_attempts'],'Carrier reconnected under pressure')
                    for k in ['active_streams','receive_credit_bytes','bootstrap_credit_bytes','growth_credit_bytes','receive_allocated_bytes','data_pending_frames','data_pending_bytes','control_pending_frames','control_pending_bytes','window_blocked_writers']:
                        peaks[who][k]=max(peaks[who].get(k,0),r[k])
                require(peaks[who]['active_streams']==target and d['peak_'+who+'_streams']==target,'Peak not supported by actual sample')
                require(reclaimed(d[who+'_after']),'Resources did not return to baseline')
                final_r=d[who+'_after']['resources']
                require(final_r['opened_streams']==final_r['closed_streams'],'Identity lifetime accounting mismatch')
            require(samples[0]['client']['lifecycle']['session_tag']==samples[0]['server']['lifecycle']['session_tag'],'Different endpoint sessions')
            for field in ['accepted','rejected']:
                require(d['admission_after'][field]==d['admission_before'][field],'Stream load triggered carrier handshake')
            require(target!=MAX_STREAMS or d['typed_next_stream_rejection'] is True,f'{MAX_STREAMS+1}th hard boundary not checked')
            require(len(d['bulk_segments'])>=6 and d['churn_reopens']>=8 and d['exchanges']>=target,'Insufficient mixed/churn load')
            for b in d['bulk_segments']:
                require(0<b['start_seconds']<b['end_seconds']<35 and b['end_seconds']-b['start_seconds']<15 and b['bytes']>0 and len(b['sha256'])==64,'Bulk did not enter/leave within the round')
            cases.append({'target_streams':target,'round':round_number,'seconds':d['observed_seconds'],
                'client_peak':peaks['client'],'server_peak':peaks['server'],'exchanges':d['exchanges'],
                'churn_reopens':d['churn_reopens'],'bulk_segments':len(d['bulk_segments']),
                'open_receive_credit_waits':0,'admission_deadline_exceeded':0,'unexpected_resource_refusals':0,
                'six_carriers_preserved':True,'same_session':True,'reclaimed':True,
                'typed_next_stream_rejection':d['typed_next_stream_rejection'],'evidence_sha256':sha(path),'passed':True})
    return {'version':VERSION,'wire_protocol':3,'source_id':identity,'verified':True,'status':'passed',
            'mode':'actual-logical-streams-30sx2','cases':cases,
            'scope':'Six shaped loopback TCP carriers; 128/256/512/1024/2048 actual simultaneous logical streams. Not HTTP request totals or public-WAN/App capacity.'}


def check(capacity: dict, runtime: dict, identity: str, *, required: bool) -> bool:
    for record in [capacity,runtime]:
        require(record.get('version')==VERSION and record.get('wire_protocol')==3 and record.get('source_id')==identity,'Acceptance record identity mismatch')
    if capacity.get('verified') is True:
        require(capacity.get('mode')=='actual-logical-streams-30sx2','Wrong capacity workload')
        cases=capacity.get('cases',[])
        require(len(cases)==len(TARGETS)*2 and {(d['target_streams'],d['round']) for d in cases}=={(n,r) for n in TARGETS for r in [1,2]},'Capacity matrix incomplete')
        for d in cases:
            require(d.get('passed') is True and 30<=d.get('seconds',0)<=35,'Capacity case failed or duration invalid')
            require(d['client_peak']['active_streams']==d['target_streams']==d['server_peak']['active_streams'],'Not actual simultaneous streams')
            for who in ['client_peak','server_peak']:
                for field,limit in {k:v for k,v in CAPS.items() if k!='active_streams'}.items():
                    require(isinstance(d[who].get(field),int) and 0<=d[who][field]<=limit,'Invalid recorded capacity peak: '+field)
            require(d.get('exchanges',0)>=d['target_streams'] and d.get('churn_reopens',0)>=8 and d.get('bulk_segments',0)>=6,'Missing mixed capacity work')
            require(d['target_streams']!=MAX_STREAMS or d.get('typed_next_stream_rejection') is True,f'Missing {MAX_STREAMS+1}th boundary proof')
            require(len(d.get('evidence_sha256',''))==64,'Missing capacity source evidence digest')
            require(all(d.get(k)==0 for k in ['open_receive_credit_waits','admission_deadline_exceeded','unexpected_resource_refusals']),'Capacity admission failure')
            require(all(d.get(k) is True for k in ['six_carriers_preserved','same_session','reclaimed']),'Capacity lifecycle failure')
    if runtime.get('verified') is True:
        require(runtime.get('workload')=='segmented-mixed-180s' and 180<=runtime.get('observed_seconds',0)<=195,'Not the three-minute physical mixed scenario')
        require(runtime.get('short_attempts',0)>=100 and runtime.get('short_failures')==0,'Insufficient/failing physical short requests')
        require(runtime.get('idle_keepalive_connections',0)>=4 and runtime.get('idle_integrity') is True,'Physical idle/keepalive not validated')
        segments=runtime.get('bulk_segments',[])
        require(len(segments)>=6,'Missing physical segmented bulk')
        for b in segments:
            require(b.get('success') is True and 0<b['start_seconds']<b['end_seconds']<195 and b['end_seconds']-b['start_seconds']<30 and b['bytes']>0,'Invalid physical bulk segment')
        require(runtime.get('minimum_sampled_paths')==6 and runtime.get('final_paths')==6,'Physical carriers not preserved')
        require(all(runtime.get(k) is True for k in ['same_session','no_carrier_reconnect','body_integrity','origin_body_hash_matched','surge_policy_verified','installed_app_child_verified','protected_services_unchanged','protected_files_unchanged','fixture_stopped','temporary_route_removed']),'Missing physical proof or cleanup')
        require(all(runtime.get(k)==0 for k in ['open_receive_credit_waits','admission_deadline_exceeded','resource_refusals']),'Physical admission failure')
    complete=capacity.get('verified') is True and runtime.get('verified') is True
    if required:require(complete,'Ten capacity cases and matching three-minute App+Surge proof are both required')
    return complete


def main() -> None:
    parser=argparse.ArgumentParser()
    parser.add_argument('--collect-capacity',type=pathlib.Path)
    args=parser.parse_args()
    out=ROOT/f'dist/userspace-{VERSION}';identity=(out/'SOURCE_ID').read_text().strip()
    spec=importlib.util.spec_from_file_location('source_manifest',ROOT/'scripts/source-manifest.py')
    source=importlib.util.module_from_spec(spec);spec.loader.exec_module(source)
    require(source.sha(source.manifest(source.collect()))==identity,'Current source differs from freeze')
    if args.collect_capacity:
        d=collect(args.collect_capacity,identity)
        (out/'CAPACITY.json').write_text(json.dumps(d,indent=2)+'\n')
        print(json.dumps({'verified':True,'source_id':identity,'cases':len(d['cases']),'capacity_sha256':sha(out/'CAPACITY.json')},indent=2))
    else:
        capacity=json.loads((out/'CAPACITY.json').read_text());runtime=json.loads((out/'RUNTIME.json').read_text())
        check(capacity,runtime,identity,required=True);print('MATCHED_SHORT_CAPACITY_AND_PHYSICAL_GATES_PASS')

if __name__=='__main__':main()
