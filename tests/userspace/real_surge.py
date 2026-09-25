#!/usr/bin/env python3
"""Opt-in live Surge validation attached to a manually started MPTCP Desk.
Reads ONLY the App's non-secret diagnostic snapshot and restricted server
status. Never reads a transport key/Keychain/profile, controls the GUI, starts
an engine, or stops the user's App. The temporary HTTP origin uses its own
operator-created token, unrelated to SS or MPX credentials.
"""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, http.client, json, os, pathlib
import socket, subprocess, threading, time

CLI='/Applications/Surge.app/Contents/Applications/surge-cli'
PATTERN=bytes((i*31+(i>>8)+17)%256 for i in range(32768))
REPEATED=PATTERN*3


def checked(args, *, timeout=20):
    p=subprocess.run(args,capture_output=True,text=True,timeout=timeout)
    if p.returncode:
        raise RuntimeError(pathlib.Path(args[0]).name+' failed: '+p.stderr[-1200:])
    return p.stdout


def properties(text):
    return dict(line.split('=',1) for line in text.splitlines() if '=' in line)


def running_app_engine() -> tuple[int,int]:
    rows=checked(['lsof','-nP','-t','-iTCP:1081','-sTCP:LISTEN']).split()
    pids=sorted(set(int(p) for p in rows))
    if len(pids)!=1:raise RuntimeError('Expected exactly one manually started App engine on 1081')
    pid=pids[0]
    command=checked(['ps','-p',str(pid),'-o','comm=']).strip()
    parent=int(checked(['ps','-p',str(pid),'-o','ppid=']).strip())
    parent_command=checked(['ps','-p',str(parent),'-o','comm=']).strip()
    if not command.endswith('/Contents/Resources/mptcp-desktop-engine') or not parent_command.endswith('/Contents/MacOS/MPTCPDesk'):
        raise RuntimeError('Listener is not owned by a running MPTCP Desk App; no process will be changed')
    return pid,parent


def read_snapshot(path: pathlib.Path, identity: str):
    if path.is_symlink() or path.stat().st_size>131072 or path.stat().st_mode&0o077:
        raise RuntimeError('Diagnostic snapshot must be a bounded owner-only ordinary file')
    if time.time()-path.stat().st_mtime>6:
        raise RuntimeError('MPTCP Desk diagnostic snapshot is stale; start the matching App manually')
    event=json.loads(path.read_text())
    if any(k in event for k in ['transport_key','password','profile']):
        raise RuntimeError('Unexpected private fields in telemetry')
    if event.get('kind')!='stats' or event.get('version')!='0.7.1' or event.get('source_id')!=identity or event.get('wire_protocol')!=2:
        raise RuntimeError('Running App telemetry does not match the frozen 0.7.1 build')
    return event


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--source-id',required=True)
    p.add_argument('--diagnostic-file',type=pathlib.Path,default=pathlib.Path.home()/'Library/Logs/MPTCPDesk/latest-transport.json')
    p.add_argument('--fixture-token-file',required=True,type=pathlib.Path)
    p.add_argument('--ssh-host',required=True)
    p.add_argument('--ssh-key',required=True,type=pathlib.Path)
    p.add_argument('--target',required=True)
    p.add_argument('--fixture-port',type=int,default=18081)
    p.add_argument('--proxy-port',type=int,default=6152)
    p.add_argument('--policy',required=True)
    p.add_argument('--run-id',required=True)
    p.add_argument('--seconds',type=int,default=1810)
    p.add_argument('--report-dir',required=True,type=pathlib.Path)
    p.add_argument('--confirm-live',action='store_true')
    a=p.parse_args()
    if not a.confirm_live or not 1<=a.seconds<=1860:raise ValueError('Explicit live confirmation and bounded duration required')
    for f in [a.fixture_token_file,a.ssh_key]:
        if f.stat().st_mode&0o077:raise ValueError('Temporary fixture credentials must be owner-only')
    pid,app_pid=running_app_engine()
    initial_client=read_snapshot(a.diagnostic_file,a.source_id)
    if initial_client.get('paths')!=6 or len(initial_client.get('path_stats',[]))!=6:
        raise RuntimeError('Manually started App must have six actual authenticated carriers')
    session_tag=initial_client['lifecycle']['session_tag']
    token=a.fixture_token_file.read_text().strip()
    report=a.report_dir;report.mkdir(parents=True,exist_ok=True)
    os.chmod(report,0o700)
    ssh=['ssh','-i',str(a.ssh_key),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=5','root@'+a.ssh_host]
    def remote(command):return checked(ssh+[command],timeout=10)
    rule=f'DEST-PORT,{a.fixture_port},{a.policy}'
    if rule in checked([CLI,'rule','temp','list']):raise RuntimeError('Test route already exists; not taking ownership')
    state={'bytes':0,'shorts':0,'short_failures':[],'long_error':None,'long_done':False,'durations':[],'samples':[]}
    lock=threading.Lock();stop=threading.Event();rule_added=False;traffic_start=0.0
    long_thread=None;burst_thread=None
    with (report/'engine-events.jsonl').open('w') as engine_log, (report/'landing-events.jsonl').open('w') as remote_log:
        try:
            checked([CLI,'rule','temp','add',rule]);rule_added=True
            explanation=checked([CLI,'rule','explain',f'http://{a.target}:{a.fixture_port}/short'])
            (report/'surge-routing.txt').write_text(explanation)
            if 'Final policy: '+a.policy not in explanation:raise RuntimeError('Surge route is not the explicit test leaf policy')
            def request(path, *, big=False):
                conn=http.client.HTTPConnection('127.0.0.1',a.proxy_port,timeout=20)
                start=time.monotonic();count=0;digest=hashlib.sha256()
                try:
                    conn.request('GET',f'http://{a.target}:{a.fixture_port}{path}',headers={'X-MPX-Test-Token':token,'Connection':'close','Proxy-Connection':'close','User-Agent':'MPTCP-Validation/0.7.1'})
                    response=conn.getresponse()
                    if response.status!=200 or response.getheader('X-MPX-Test-ID')!=a.run_id:
                        raise RuntimeError(f'Unexpected fixture response {response.status}')
                    if path=='/health':return json.loads(response.read())
                    while True:
                        data=response.read(65536)
                        if not data:break
                        offset=count%len(PATTERN)
                        if data!=REPEATED[offset:offset+len(data)]:raise RuntimeError('Payload bytes changed')
                        count+=len(data);digest.update(data)
                        if big:
                            with lock:state['bytes']=count
                    if path.startswith('/short') and count!=1537:raise RuntimeError('Short response length changed')
                    return {'bytes':count,'seconds':time.monotonic()-start,'sha256':digest.hexdigest()}
                finally:conn.close()
            health=request('/health')
            initial_landing=json.loads(remote('status'))
            if initial_landing.get('source_id')!=a.source_id:raise RuntimeError('Landing does not match the App source identity')
            initial_unit=remote('unit');(report/'unit-before.txt').write_text(initial_unit)
            calibration=request('/bytes?n=16777216')
            time.sleep(1.2)
            baseline=read_snapshot(a.diagnostic_file,a.source_id)
            if baseline.get('received',0)-initial_client.get('received',0)<calibration['bytes']:
                raise RuntimeError('Test payload bypassed the observed App engine')
            traffic_start=time.monotonic()
            def big_worker():
                try:
                    result=request('/stream?seconds='+str(a.seconds),big=True)
                    with lock:state['long_result']=result
                except Exception as exc:
                    with lock:state['long_error']=str(exc)
                finally:
                    with lock:state['long_done']=True
            def short_worker(index):
                try:
                    result=request('/short?i='+str(index))
                    with lock:state['shorts']+=1;state['durations'].append(result['seconds']*1000)
                except Exception as exc:
                    with lock:state['shorts']+=1;state['short_failures'].append({'index':index,'error':str(exc)})
            def bursts():
                index=0
                with concurrent.futures.ThreadPoolExecutor(max_workers=32) as pool:
                    while not stop.wait(10):
                        if time.monotonic()-traffic_start>=a.seconds-8:return
                        size=32 if index%6==5 else 8
                        jobs=[pool.submit(short_worker,index*32+i) for i in range(size)]
                        for job in jobs:job.result()
                        index+=1
            long_thread=threading.Thread(target=big_worker,daemon=True);burst_thread=threading.Thread(target=bursts,daemon=True)
            long_thread.start();burst_thread.start()
            previous_time=traffic_start;previous_bytes=0;next_sample=traffic_start
            while True:
                with lock:done=state['long_done'];failure=state['long_error']
                if failure:raise RuntimeError('Long flow failed: '+failure)
                if done:break
                os.kill(pid,0);os.kill(app_pid,0)
                now=time.monotonic()
                if now>=next_sample:
                    stats=read_snapshot(a.diagnostic_file,a.source_id)
                    if stats['lifecycle']['session_tag']!=session_tag:raise RuntimeError('App restarted the logical session')
                    landing=json.loads(remote('status'))
                    if landing.get('source_id')!=a.source_id:raise RuntimeError('Landing source changed')
                    for log,event in [(engine_log,stats),(remote_log,landing)]:log.write(json.dumps({'observed_utc':time.time(),'snapshot':event})+'\n');log.flush()
                    peers=[s for s in landing.get('tcp',[]) if s.get('lifecycle',{}).get('session_tag')==session_tag]
                    if len(peers)!=1:raise RuntimeError('Matching server-side session missing or ambiguous')
                    for resources in [stats['resources'],peers[0]['resources']]:
                        for field,bound in [('receive_credit_bytes',20<<20),('receive_allocated_bytes',32<<20),('pending_bytes',32<<20),('pending_frames',4096)]:
                            if not 0<=resources.get(field,0)<=bound:raise RuntimeError('Resource accounting outside hard bound')
                    with lock:n=state['bytes'];attempts=state['shorts'];failed=len(state['short_failures'])
                    resource=stats['resources']
                    sample={'seconds':now-traffic_start,'body_bytes':n,'short_attempts':attempts,'short_failures':failed,'paths':stats['paths'],'credit':resource['receive_credit_bytes'],'allocated':resource['receive_allocated_bytes'],'pending_frames':resource['pending_frames'],'rejections':resource['rejections'],'server_rejections':peers[0]['resources']['rejections']}
                    state['samples'].append(sample)
                    if now-previous_time>=29:
                        print(json.dumps({'LIVE_PROGRESS':round(now-traffic_start,1),'body_gib':round(n/(1<<30),3),'interval_mbps':round((n-previous_bytes)*8/(now-previous_time)/1e6,2),'shorts':attempts,'failures':failed,'paths':stats['paths']}),flush=True)
                        previous_time,previous_bytes=now,n
                    next_sample=now+10
                time.sleep(.2)
            stop.set();burst_thread.join(30);long_thread.join(5)
            if burst_thread.is_alive():raise RuntimeError('Short workers did not finish')
            time.sleep(3)
            final=read_snapshot(a.diagnostic_file,a.source_id)
            server=json.loads(remote('status'));unit=remote('unit')
            result=state['long_result']
            if state['short_failures']:raise RuntimeError('Some new short connections failed')
            if result['seconds']<a.seconds-2:raise RuntimeError('Continuous flow ended prematurely')
            if final['received']-baseline['received']<result['bytes']:raise RuntimeError('Long payload bypassed the observed engine')
            if final['paths']!=6 or final['lifecycle']['session_tag']!=session_tag:raise RuntimeError('Six-carrier session not preserved')
            old,new=properties(initial_unit),properties(unit)
            if old.get('MainPID')!=new.get('MainPID') or old.get('NRestarts')!=new.get('NRestarts'):raise RuntimeError('Landing restarted')
            for sample in state['samples']:
                if any(sample['rejections'].values()) or any(sample['server_rejections'].values()):raise RuntimeError('Normal mixed traffic hit a resource refusal')
            public={'verified':True,'version':'0.7.1','source_id':a.source_id,'scope':'Manually started MPTCP Desk App and its existing engine, actual Surge leaf policy, six real relays and existing Landing/SS. Authenticated generated bytes; no GUI automation or credential reads.','gui_automation':False,'continuous_seconds':result['seconds'],'body_bytes':result['bytes'],'body_sha256':result['sha256'],'body_integrity':True,'average_mbps':result['bytes']*8/result['seconds']/1e6,'short_attempts':state['shorts'],'short_failures':0,'p95_short_ms':sorted(state['durations'])[int(.95*len(state['durations']))] if state['durations'] else None,'configured_paths':6,'final_paths':6,'resource_refusals':0,'landing_restarted':False,'resource_peak':{k:max(s[k] for s in state['samples']) for k in ['credit','allocated','pending_frames']}}
            (report/'RUNTIME.json').write_text(json.dumps(public,indent=2)+'\n')
            (report/'RUNTIME.private.json').write_text(json.dumps(dict(public,client_after=final,landing_after=server,samples=state['samples'],fixture_before=health,fixture_after=request('/health')),indent=2)+'\n')
            print(json.dumps(public,indent=2),flush=True)
        except Exception as exc:
            (report/'FAILED.json').write_text(json.dumps({'verified':False,'error':str(exc),'source_id':a.source_id,'elapsed_seconds':time.monotonic()-traffic_start if traffic_start else 0,'body_bytes':state['bytes'],'short_attempts':state['shorts'],'short_failures':state['short_failures']},indent=2)+'\n')
            raise
        finally:
            stop.set()
            for thread in [long_thread,burst_thread]:
                if thread is not None:thread.join(25)
            if rule_added:checked([CLI,'rule','temp','remove',rule])
            (report/'cleanup.json').write_text(json.dumps({'attached_engine_pid':pid,'app_pid':app_pid,'user_app_not_stopped':True,'test_rule_removed':rule_added},indent=2)+'\n')

if __name__=='__main__':main()
