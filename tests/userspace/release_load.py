#!/usr/bin/env python3
"""Actual Mac executable -> six TCP relays -> Debian amd64 systemd executable.
Only the pre-provisioned disposable guest is changed, with SS backend restored.
Measures QEMU/TCG separately from the controlled high-BDP Go test matrix.
"""
from __future__ import annotations
import hashlib
import json
import os
import pathlib
import socket
import subprocess
import threading
import time
from live_ss import Guest, ROOT, REPORT, VERSION, DIST, Relay, port, pcb_count, checked

ECHO = '''import socket,threading
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',29081));s.listen(32)
def echo(c):
    try:
        c.settimeout(120)
        while True:
            b=c.recv(262144)
            if not b: break
            c.sendall(b)
        c.shutdown(socket.SHUT_WR)
    except OSError: pass
    finally: c.close()
while True:
    c,a=s.accept();threading.Thread(target=echo,args=(c,),daemon=True).start()
'''


def transfer(number: int, size: int) -> dict:
    c=socket.create_connection(('127.0.0.1',number),timeout=15)
    c.settimeout(90)
    payload=os.urandom(size)
    expected=hashlib.sha256(payload).hexdigest()
    errors=[]
    started=time.monotonic()
    def write():
        try:
            for offset in range(0,size,262144):
                c.sendall(payload[offset:offset+262144])
            c.shutdown(socket.SHUT_WR)
        except Exception as exc:
            errors.append(str(exc))
    worker=threading.Thread(target=write)
    worker.start()
    digest=hashlib.sha256();received=0
    try:
        while True:
            data=c.recv(262144)
            if not data:break
            digest.update(data);received+=len(data)
    finally:
        c.close();worker.join(100)
    elapsed=time.monotonic()-started
    if worker.is_alive() or errors or received!=size or digest.hexdigest()!=expected:
        raise RuntimeError(f'Integrity failure: bytes={received}/{size}, sender={errors}')
    return {'bytes':size,'seconds':elapsed,'mbps':size*8/elapsed/1e6,'sha256':expected,'integrity':True}


def main() -> None:
    REPORT.mkdir(parents=True,exist_ok=True)
    g=Guest()
    original=json.loads((g.directory/'landing-private.json').read_text())
    config=dict(original,backend_tcp='127.0.0.1:29081',udp_enabled=False)
    g.upload(g.private('landing-load-private.json',config),'landing-load.json')
    g.upload(g.directory/'landing-private.json','landing-restore.json')
    echo=g.directory/'bulk-echo.py';echo.write_text(ECHO);g.upload(echo,'bulk-echo.py')
    g.run('sudo systemd-run --unit=mptcp-validation-load --property=User=tester /usr/bin/python3 '+g.destination+'/bulk-echo.py')
    relays=[];engine=None;collector=None;events=[];ready=threading.Event();usage=[]
    stop_sample=threading.Event();sampler=None
    before=pcb_count()
    engine_path=pathlib.Path(os.environ.get('MPX_TEST_ENGINE',str(ROOT/'macos/build/engine-arm64')))
    try:
        g.run('sudo /usr/local/bin/mptcp-landing config --source '+g.destination+'/landing-load.json')
        identity=json.loads(checked(['/usr/bin/arch','-arm64',str(engine_path),'version']))
        assert identity.get('version')==VERSION and identity.get('wire_protocol')==2,identity
        direct=transfer(g.meta['echo_port'],16<<20)
        relays=[Relay(g.meta['landing_port']) for _ in range(6)]
        entrance=port()
        profile={'schema_version':3,'mode':'userspace_multipath','listen_port':entrance,'relays':[{'host':'127.0.0.1','port':r.port} for r in relays],'tcp_enabled':True,'udp_enabled':False,'transport_key':original['transport_key']}
        engine=subprocess.Popen(['/usr/bin/arch','-arm64',str(engine_path),'run'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
        engine.stdin.write(json.dumps(profile));engine.stdin.close()
        def collect():
            for line in engine.stdout:
                try:event=json.loads(line)
                except ValueError:continue
                events.append(event)
                if event.get('kind')=='listening':ready.set()
        collector=threading.Thread(target=collect,daemon=True);collector.start()
        if not ready.wait(20):raise RuntimeError('Engine did not start: '+json.dumps(events[-4:]))
        deadline=time.monotonic()+10
        while time.monotonic()<deadline:
            if any(e.get('kind')=='stats' and e.get('paths')==6 for e in events):break
            time.sleep(.1)
        else:raise RuntimeError('Not all six carriers became active')
        def sample():
            while not stop_sample.wait(.5):
                result=subprocess.run(['ps','-o','rss=,cputime=','-p',str(engine.pid)],capture_output=True,text=True)
                usage.append(result.stdout.strip())
        sampler=threading.Thread(target=sample,daemon=True);sampler.start()
        warmup=transfer(entrance,16<<20)
        measurements=[transfer(entrance,64<<20) for _ in range(2)]
        deadline=time.monotonic()+6
        landing=None;stats={}
        while time.monotonic()<deadline:
            time.sleep(.5)
            stats=next((e for e in reversed(events) if e.get('kind')=='stats'),{})
            landing=json.loads(g.run('sudo cat /run/mptcp-userspace-landing/status.json'))
            fields=['connections','pending_bytes','reorder_bytes','receive_allocated_bytes','receive_credit_bytes','ready_frames']
            if all(stats.get(k,0)==0 for k in fields) and all(all(s.get(k,0)==0 for k in fields) for s in landing['tcp']):break
        else:raise RuntimeError('Post-load state was not reclaimed')
        assert stats.get('paths')==6 and all(p['sent']>0 and p['received']>0 for p in stats['path_stats']),stats
        assert landing['source_id']==identity['source_id'], 'Mac and Landing Source-ID differ'
        assert before==pcb_count(), 'Native PCB changed during Userspace validation'
        machine=g.run('uname -m; sudo systemctl show mptcp-userspace-landing.service -p MainPID -p MemoryPeak -p CPUUsageNSec -p DynamicUser; sha256sum /usr/local/bin/mptcp-landing').decode()
        report={'version':VERSION,'source_id':identity['source_id'],'engine_sha256':hashlib.sha256(engine_path.read_bytes()).hexdigest(),'landing_sha256':hashlib.sha256((DIST/'mptcp-landing').read_bytes()).hexdigest(),'scope':'Actual Mac arm64 executable -> six unshaped transparent TCP relays -> QEMU/TCG Debian 13 amd64 systemd Landing -> TCP echo; not an in-process test, not a WAN or native-x86 hardware speed claim','qemu_plain_tcp_baseline':direct,'warmup':warmup,'single_stream_measurements':measurements,'engine_after':stats,'landing_after':landing,'process_rss_kib_cpu_samples':usage,'landing_process':machine,'pcb_before':before,'pcb_after':pcb_count(),'limitations':'QEMU TCG translates x86_64 on arm64 and carries all six paths over one host-forward backend; measured speed is not the controlled 500Mbps matrix capacity'}
        (REPORT/'release-process-load.json').write_text(json.dumps(report,indent=2)+'\n')
        (REPORT/'release-process-load-events.jsonl').write_text('\n'.join(json.dumps(e) for e in events)+'\n')
        print(json.dumps({k:report[k] for k in ['version','source_id','qemu_plain_tcp_baseline','single_stream_measurements','pcb_before','pcb_after','scope']},indent=2))
    finally:
        stop_sample.set()
        if sampler:sampler.join(3)
        if engine and engine.poll() is None:
            engine.terminate()
            try:engine.wait(timeout=15)
            except subprocess.TimeoutExpired:engine.kill();engine.wait(timeout=5)
        if collector:collector.join(2)
        for relay in relays:relay.close()
        try:g.run('sudo /usr/local/bin/mptcp-landing config --source '+g.destination+'/landing-restore.json')
        finally:g.run('sudo systemctl stop mptcp-validation-load.service')
        print('Load-test engine/relays stopped; disposable Landing restored to SS backend')


if __name__=='__main__':main()
