#!/usr/bin/env python3
"""Opt-in 180s physical mixed test of an existing, user-started 0.9.0 App.
Never starts an engine/App, reads a key/profile, or controls the desktop.
Finalization requires a separate saved-VPS origin/protected-service audit.
"""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, http.client, importlib.util, json, os
import pathlib, plistlib, re, subprocess, threading, time
ROOT=pathlib.Path(__file__).resolve().parents[2]
OUT=ROOT/'dist/userspace-0.9.0'
CLI='/Applications/Surge.app/Contents/Applications/surge-cli'
PATTERN=bytes((i*31+(i>>8)+17)%256 for i in range(32768));REPEATED=PATTERN*3


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    loaded=importlib.util.module_from_spec(spec);spec.loader.exec_module(loaded);return loaded
G=module('release_gates',ROOT/'scripts/release-gates.py')
S=module('source_manifest',ROOT/'scripts/source-manifest.py')


def checked(args,timeout=15):
    r=subprocess.run(args,capture_output=True,text=True,timeout=timeout)
    if r.returncode:raise RuntimeError(pathlib.Path(args[0]).name+' failed: '+r.stderr[-800:])
    return r.stdout


def properties(text):return dict(line.split('=',1) for line in text.splitlines() if '=' in line)


def identity():
    value=(OUT/'SOURCE_ID').read_text().strip()
    G.require(S.sha(S.manifest(S.collect()))==value,'Current source differs from release freeze')
    return value


def app_owner():
    pids=set(int(x) for x in checked(['lsof','-nP','-t','-iTCP:1081','-sTCP:LISTEN']).split())
    G.require(len(pids)==1,'Expected one existing user-started App engine at 1081')
    pid=next(iter(pids));command=checked(['ps','-p',str(pid),'-o','comm=']).strip()
    parent=int(checked(['ps','-p',str(pid),'-o','ppid=']).strip())
    owner=checked(['ps','-p',str(parent),'-o','comm=']).strip()
    G.require(command.endswith('/Contents/Resources/mptcp-desktop-engine') and owner.endswith('/Contents/MacOS/MPTCPDesk'),'Listener is not owned by the actual App')
    info=plistlib.loads((pathlib.Path(owner).parents[1]/'Info.plist').read_bytes())
    G.require(info['CFBundleShortVersionString']=='0.9.0' and info['MPTCPSourceID']==identity(),'Installed App does not match this 0.9.0 freeze')
    return pid,parent


def snapshot(path,expected):
    st=path.stat()
    G.require(path.is_file() and not path.is_symlink() and st.st_size<=131072 and st.st_mode&0o077==0,'Diagnostic file must be bounded and owner-only')
    G.require(time.time()-st.st_mtime<6,'App diagnostic snapshot is stale; use the matching user-started App')
    d=json.loads(path.read_text())
    G.require(d.get('kind')=='stats' and d.get('version')=='0.9.0' and d.get('wire_protocol')==3 and d.get('source_id')==expected,'App telemetry identity mismatch')
    G.require(not any(k in d for k in ['password','transport_key','profile']),'Private fields in diagnostics')
    return d


def run(a):
    G.require(a.confirm_live and re.fullmatch(r'[A-Za-z0-9_-]{1,80}',a.run_id),'Explicit live confirmation and safe run ID required')
    G.require(a.policy and not any(c in a.policy for c in ',\r\n'),'Invalid policy name')
    expected=identity();pid,app_pid=app_owner();initial=snapshot(a.diagnostic,expected)
    G.require(initial['paths']==6,'Six existing App carriers are required')
    report=a.report_dir;report.mkdir(parents=True,exist_ok=False);os.chmod(report,0o700)
    rule=f'DEST-PORT,{a.fixture_port},{a.policy}';target=f'http://127.0.0.1:{a.fixture_port}'
    original_rules=checked([CLI,'rule','temp','list']);G.require(rule not in original_rules,'Test route already exists and is not owned')
    (report/'rules-before.txt').write_text(original_rules)
    lock=threading.Lock();stop=threading.Event();connections=set();threads=[];rule_added=False;began=0
    responses=[];failures=[];client_rows=[];server_rows=[]
    def request(path):
        conn=http.client.HTTPConnection('127.0.0.1',a.proxy_port,timeout=20)
        start=time.monotonic();count=0;digest=hashlib.sha256()
        with lock:connections.add(conn)
        try:
            conn.request('GET',target+path,headers={'Connection':'close','Proxy-Connection':'close','User-Agent':'MPTCP-Mixed-Validation/0.9.0'})
            response=conn.getresponse()
            G.require(response.status==200 and response.getheader('X-MPX-Test-ID')==a.run_id,'Unexpected fixture response')
            if path in ['/health','/telemetry']:
                raw=response.read(131073);G.require(len(raw)<=131072,'Oversized fixture diagnostics');return json.loads(raw)
            while not stop.is_set():
                data=response.read(64 if path.startswith('/hold') else 65536)
                if not data:break
                offset=count%len(PATTERN)
                G.require(data==REPEATED[offset:offset+len(data)],'Payload integrity mismatch')
                count+=len(data);digest.update(data)
            G.require(not stop.is_set(),'Cancelled response')
            if path.startswith('/short'):G.require(count==1537,'Short response length differs')
            if path.startswith('/bytes'):G.require(count==int(path.split('n=',1)[1].split('&',1)[0]),'Medium response length differs')
            result={'path':path,'start_seconds':start-began,'end_seconds':time.monotonic()-began,'bytes':count,'sha256':digest.hexdigest(),'success':True}
            with lock:responses.append(result)
            return result
        finally:
            conn.close()
            with lock:connections.discard(conn)
    def guarded(fn):
        try:fn()
        except Exception as e:
            with lock:failures.append(str(e))
    def pause_until(at):
        return not stop.wait(max(0,began+at-time.monotonic()))
    try:
        checked([CLI,'rule','temp','add',rule]);rule_added=True
        routing=checked([CLI,'rule','explain',target+'/health']);(report/'routing.txt').write_text(routing)
        G.require('Final policy: '+a.policy in routing,'Traffic did not resolve to the requested existing Surge leaf')
        health=request('/health');remote=request('/telemetry')
        G.require(remote['status']['version']=='0.9.0' and remote['status']['source_id']==expected and remote['status']['wire_protocol']==3,'Landing identity mismatch')
        tag=initial['lifecycle']['session_tag'];peer=next(s for s in remote['status']['tcp'] if s['lifecycle']['session_tag']==tag)
        before_unit=properties(remote['unit']);before_peer=peer
        # Calibrate that these generated bytes pass through the observed App.
        calibration=request('/bytes?n=1048576&i=calibration');time.sleep(1.2)
        baseline=snapshot(a.diagnostic,expected)
        G.require(baseline['received']-initial['received']>=calibration['bytes'],'Fixture payload bypassed the observed App')
        with lock:responses.clear()
        before={'client':baseline,'landing':remote,'app_pid':app_pid,'engine_pid':pid,'health':health}
        (report/'initial.private.json').write_text(json.dumps(before,indent=2)+'\n')
        began=time.monotonic()
        def holds(index):request(f'/hold?seconds=180&i=hold-{index}')
        def bulk(actor):
            for phase,start in enumerate([8,48,90,132]):
                if not pause_until(start+actor*5.7):return
                seconds=[7,11,9,8][(actor+phase)%4]
                request(f'/bulk?seconds={seconds}&i=bulk-{actor}-{phase}')
        def shorts():
            with concurrent.futures.ThreadPoolExecutor(max_workers=32) as pool:
                for wave in range(32):
                    if not pause_until(3+wave*5.2) or time.monotonic()-began>174:return
                    size=[8,16,32][wave%3]
                    jobs=[pool.submit(request,f'/short?i=short-{wave}-{i}') for i in range(size)]
                    for job in jobs:job.result()
        def mediums():
            for wave in range(23):
                if not pause_until(5+wave*7.1):return
                request(f'/bytes?n={262144+wave%3}&i=medium-{wave}')
        work=[lambda i=i:holds(i) for i in range(6)]+[lambda i=i:bulk(i) for i in range(2)]+[shorts,mediums]
        for fn in work:
            thread=threading.Thread(target=lambda fn=fn:guarded(fn),daemon=True);threads.append(thread);thread.start()
        next_server=began;next_print=began+30
        with (report/'client-samples.jsonl').open('w') as cl,(report/'landing-samples.jsonl').open('w') as sl:
            while time.monotonic()-began<180:
                now=time.monotonic();os.kill(pid,0);os.kill(app_pid,0)
                with lock:G.require(not failures,'Mixed worker failure: '+str(failures[:2]))
                current=snapshot(a.diagnostic,expected);G.bounds(current)
                G.require(current['paths']==6 and current['lifecycle']['session_tag']==tag and not current['lifecycle']['closed'],'App session/carriers changed')
                G.require(current['resources']['rejections']==baseline['resources']['rejections'],'App resource refusal')
                row={'seconds':now-began,'snapshot':current};client_rows.append(row);cl.write(json.dumps(row)+'\n');cl.flush()
                if now>=next_server:
                    remote=request('/telemetry');server=remote['status']
                    G.require(server['source_id']==expected,'Landing source changed')
                    peer=next(s for s in server['tcp'] if s['lifecycle']['session_tag']==tag);G.bounds(peer)
                    G.require(peer['paths']==6 and not peer['lifecycle']['closed'] and peer['resources']['rejections']==before_peer['resources']['rejections'],'Landing session/resource failure')
                    unit=properties(remote['unit']);G.require(all(unit.get(k)==before_unit.get(k) for k in ['MainPID','NRestarts','ActiveState','SubState']),'Landing restarted')
                    row={'seconds':now-began,'remote':remote};server_rows.append(row);sl.write(json.dumps(row)+'\n');sl.flush();next_server=now+5
                if now>=next_print:
                    with lock:short_count=sum(r['path'].startswith('/short') for r in responses);bulk_count=sum(r['path'].startswith('/bulk') for r in responses)
                    print(json.dumps({'mixed_seconds':round(now-began,1),'shorts_completed':short_count,'bulk_segments_completed':bulk_count,'actual_mpx_streams':current['connections'],'paths':current['paths'],'credit':current['resources']['receive_credit_bytes']}),flush=True);next_print=now+30
                stop.wait(.8)
        observed=time.monotonic()-began
        for thread in threads:thread.join(max(0,began+195-time.monotonic()))
        G.require(not any(t.is_alive() for t in threads) and not failures,'Mixed workers failed or exceeded the bounded end')
        final=snapshot(a.diagnostic,expected);remote_final=request('/telemetry')
        G.require(final['lifecycle']['session_tag']==tag and final['paths']==6,'Final session not preserved')
        old_paths={p['id']:p for p in baseline['path_stats']};deltas=[]
        for p in final['path_stats']:
            old=old_paths[p['id']];deltas.append({'id':p['id'],'received':p['received']-old['received'],'dial_attempts':p['dial_attempts']-old['dial_attempts'],'connections':p['carrier_connections']-old['carrier_connections']})
        G.require(all(p['received']>0 and p['dial_attempts']==0 and p['connections']==0 for p in deltas),'A carrier was unused or reconnected')
        short=[r for r in responses if r['path'].startswith('/short')];hold=[r for r in responses if r['path'].startswith('/hold')];bulks=[r for r in responses if r['path'].startswith('/bulk')]
        G.require(len(short)>=100 and len(hold)==6 and len(bulks)==8,'Mixed workload incomplete')
        G.require(final['received']-baseline['received']>=sum(r['bytes'] for r in responses),'Payload bypassed the observed engine')
        latencies=sorted((r['end_seconds']-r['start_seconds'])*1000 for r in short)
        result={'version':'0.9.0','wire_protocol':3,'source_id':expected,'verified':False,'status':'local-passed-awaiting-origin-audit',
            'workload':'segmented-mixed-180s','observed_seconds':observed,'short_attempts':len(short),'short_failures':0,
            'p95_short_ms':latencies[min(len(latencies)-1,int(len(latencies)*.95))],'bulk_segments':bulks,
            'idle_keepalive_connections':len(hold),'idle_integrity':True,'body_integrity':True,
            'actual_mpx_peak':max(r['snapshot']['connections'] for r in client_rows),'http_counts_are_not_mpx_stream_counts':True,
            'minimum_sampled_paths':min(r['snapshot']['paths'] for r in client_rows),'final_paths':6,'same_session':True,
            'no_carrier_reconnect':True,'carrier_deltas':deltas,'installed_app_child_verified':True,'surge_policy_verified':True,
            'open_receive_credit_waits':0,'admission_deadline_exceeded':0,'resource_refusals':0,
            'runner_sha256':G.sha(pathlib.Path(__file__)),'fixture_sha256':G.sha(ROOT/'tests/userspace/mixed_fixture.pl'),
            'scope':'Existing user-started App and existing Surge leaf via six public Relay carriers and native Landing/backend; generated loopback-origin mixed traffic, not a distinct-MPX-stream count inferred from HTTP requests.'}
        (report/'RESULT.local.json').write_text(json.dumps(result,indent=2)+'\n')
        (report/'responses.json').write_text(json.dumps(responses,indent=2)+'\n')
        (report/'final.private.json').write_text(json.dumps({'client':final,'landing':remote_final},indent=2)+'\n')
        print(json.dumps(result,indent=2),flush=True)
    except Exception as e:
        (report/'FAILED.json').write_text(json.dumps({'error':str(e),'worker_failures':failures,'verified':False,'source_id':expected},indent=2)+'\n');raise
    finally:
        stop.set()
        with lock:active=list(connections)
        for conn in active:conn.close()
        for thread in threads:thread.join(3)
        if rule_added:checked([CLI,'rule','temp','remove',rule])
        after=checked([CLI,'rule','temp','list'])
        (report/'cleanup.json').write_text(json.dumps({'test_rule_removed':rule not in after,'rules_unchanged':after==original_rules,'app_not_stopped':True},indent=2)+'\n')
        G.require(rule not in after and after==original_rules,'Temporary Surge route cleanup failed')


def finalize(a):
    expected=identity();report=a.report_dir
    result=json.loads((report/'RESULT.local.json').read_text());proof=json.loads(a.origin_proof.read_text())
    G.require(not (report/'FAILED.json').exists(),'Recorded physical failure')
    G.require(result['source_id']==expected==proof['source_id'] and result['status']=='local-passed-awaiting-origin-audit','Physical proof identity/state mismatch')
    G.require(result['runner_sha256']==G.sha(pathlib.Path(__file__)) and proof['fixture_sha256']==result['fixture_sha256'],'Validation code changed')
    cleanup=json.loads((report/'cleanup.json').read_text());G.require(cleanup['test_rule_removed'] and cleanup['rules_unchanged'],'Route not restored')
    G.require(all(proof.get(k) is True for k in ['protected_files_unchanged','protected_services_unchanged','fixture_stopped']),'Origin/protected-service cleanup audit failed')
    responses=json.loads((report/'responses.json').read_text())
    G.require(proof['response_count']==proof['unique_response_count']==len(responses) and proof['all_responses_success'] is True,'Origin/client response inventory differs')
    manifest=''.join(f"{r['path']}\t{r['bytes']}\t{r['sha256']}\t1\n" for r in sorted(responses,key=lambda r:r['path']))
    G.require(hashlib.sha256(manifest.encode()).hexdigest()==proof['response_manifest_sha256'],'Independent origin payload manifests differ')
    before=json.loads((report/'initial.private.json').read_text());tag=before['client']['lifecycle']['session_tag']
    client=[json.loads(line) for line in (report/'client-samples.jsonl').read_text().splitlines()]
    server=[json.loads(line) for line in (report/'landing-samples.jsonl').read_text().splitlines()]
    G.require(len(client)>=150 and len(server)>=30 and client[-1]['seconds']>=178 and server[-1]['seconds']>=170,'Physical sampling interval missing')
    final=json.loads((report/'final.private.json').read_text())
    old_paths={p['id']:p for p in before['client']['path_stats']}
    first_server=next(st for st in before['landing']['status']['tcp'] if st['lifecycle']['session_tag']==tag)
    first_unit=properties(before['landing']['unit'])
    for row in client+[{'snapshot':final['client']}]:
        st=row['snapshot'];G.bounds(st);G.require(st['source_id']==expected and st['lifecycle']['session_tag']==tag and st['paths']==6 and not st['lifecycle']['closed'],'App timeline mismatch')
        G.require(st['resources']['rejections']==before['client']['resources']['rejections'],'Recorded client resource refusal')
        for path in st['path_stats']:
            old=old_paths[path['id']]
            G.require(path['carrier_connections']==old['carrier_connections'] and path['dial_attempts']==old['dial_attempts'],'Recorded client carrier reconnected')
    for row in server+[{'remote':final['landing']}]:
        status=row['remote']['status'];G.require(status['source_id']==expected,'Server source mismatch')
        st=next(s for s in status['tcp'] if s['lifecycle']['session_tag']==tag);G.bounds(st);G.require(st['paths']==6 and not st['lifecycle']['closed'],'Server timeline mismatch')
        G.require(st['resources']['rejections']==first_server['resources']['rejections'],'Recorded server resource refusal')
        unit=properties(row['remote']['unit'])
        G.require(all(unit.get(k)==first_unit.get(k) for k in ['MainPID','NRestarts','ActiveState','SubState']),'Recorded server restarted')
    provenance=json.loads((OUT/'PROVENANCE.json').read_text());G.require(provenance['verified'] and provenance['source_id']==expected,'Build provenance missing')
    for name,digest in provenance['artifact_sha256'].items():G.require(G.sha(OUT/name)==digest,'Changed release artifact')
    G.require(proof['landing_sha256']==G.sha(OUT/'mptcp-landing'),'Deployed Landing differs from release')
    result.update(verified=True,status='passed',origin_body_hash_matched=True,protected_services_unchanged=True,
        protected_files_unchanged=True,fixture_stopped=True,temporary_route_removed=True,
        evidence_sha256={name:G.sha(report/name) for name in ['RESULT.local.json','responses.json','client-samples.jsonl','landing-samples.jsonl','cleanup.json']},
        origin_proof_sha256=G.sha(a.origin_proof),artifact_sha256=provenance['artifact_sha256'])
    capacity=json.loads((OUT/'CAPACITY.json').read_text());G.check(capacity,result,expected,required=True)
    (OUT/'RUNTIME.json').write_text(json.dumps(result,indent=2)+'\n');(report/'RUNTIME.final.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'verified':True,'source_id':expected,'observed_seconds':result['observed_seconds'],'shorts':result['short_attempts'],'bulk_segments':len(result['bulk_segments']),'actual_mpx_peak':result['actual_mpx_peak']},indent=2))


def main():
    p=argparse.ArgumentParser();p.add_argument('action',choices=['run','finalize']);p.add_argument('--report-dir',type=pathlib.Path,required=True)
    p.add_argument('--confirm-live',action='store_true');p.add_argument('--policy',default='');p.add_argument('--run-id',default='mpx080-mixed')
    p.add_argument('--fixture-port',type=int,default=18081);p.add_argument('--proxy-port',type=int,default=6152)
    p.add_argument('--diagnostic',type=pathlib.Path,default=pathlib.Path.home()/'Library/Logs/MPTCPDesk/latest-transport.json');p.add_argument('--origin-proof',type=pathlib.Path)
    a=p.parse_args()
    if a.action=='run':run(a)
    else:
        G.require(a.origin_proof is not None,'Independent saved-VPS origin proof is required');finalize(a)

if __name__=='__main__':main()
