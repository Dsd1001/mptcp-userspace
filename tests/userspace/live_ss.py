#!/usr/bin/env python3
"""Opt-in integration harness for the disposable Debian VM, never a saved VPS.
Uses macos/build/debian-vm.json and verified validation-tools.json.
No credentials or generated private configurations are included in reports.
"""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, json, os, pathlib, secrets
import signal, socket, struct, subprocess, threading, time

ROOT = pathlib.Path(__file__).resolve().parents[2]
VERSION = (ROOT / 'macos/VERSION').read_text().strip()
DIST = ROOT / 'dist' / ('userspace-' + VERSION)
REPORT = pathlib.Path(os.environ.get('MPX_REPORT_DIR', str(ROOT / 'reports' / ('userspace-' + VERSION))))


def checked(args, *, input=None, timeout=90):
    result = subprocess.run(args, input=input, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{pathlib.Path(args[0]).name} exit={result.returncode}: '+result.stderr.decode(errors='replace')[-3000:]+result.stdout.decode(errors='replace')[-3000:])
    return result.stdout


class Guest:
    def __init__(self):
        self.meta=json.loads((ROOT/'macos/build/debian-vm.json').read_text())
        self.tools=json.loads((ROOT/'macos/build/validation-tools.json').read_text())
        self.directory=pathlib.Path(self.meta['directory'])
        self.ssh=['ssh','-p',str(self.meta['ssh_port']),'-i',self.meta['ssh_key'],'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+self.meta['known_hosts'],'tester@127.0.0.1']
        self.destination='/home/tester/mpx-validation'
    def run(self,command,input=None,timeout=90):
        return checked(self.ssh+[command],input=input,timeout=timeout)
    def upload(self,path,name):
        checked(['scp','-q','-P',str(self.meta['ssh_port']),'-i',self.meta['ssh_key'],'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+self.meta['known_hosts'],str(path),'tester@127.0.0.1:'+self.destination+'/'+name])
    def private(self,name,data):
        p=self.directory/name
        fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
        with os.fdopen(fd,'w') as f: json.dump(data,f)
        os.chmod(p,0o600)
        return p
    def executable(self,platform,name):
        directory=pathlib.Path(next(a['directory'] for a in self.tools['assets'] if a['platform']==platform))
        return next(directory.rglob(name))


ECHO_SCRIPT='''import socket,threading
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('127.0.0.1',29080));s.listen(128)
u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);u.bind(('127.0.0.1',29080))
def udp():
    while True:
        b,a=u.recvfrom(65536);u.sendto(b,a)
def client(c):
    try:
        c.settimeout(60)
        while True:
            b=c.recv(65536)
            if not b: break
            c.sendall(b)
        c.shutdown(socket.SHUT_WR)
    except OSError: pass
    finally: c.close()
threading.Thread(target=udp,daemon=True).start()
while True:
    c,a=s.accept();threading.Thread(target=client,args=(c,),daemon=True).start()
'''


def setup(g:Guest):
    g.run('mkdir -p '+g.destination+'; chmod 700 '+g.destination)
    secret=secrets.token_hex(32)
    landing={'schema_version':1,'listen_tcp':'0.0.0.0:24001','listen_udp':'0.0.0.0:24001','backend_tcp':'127.0.0.1:8388','backend_udp':'127.0.0.1:8388','udp_enabled':True,'transport_key':secrets.token_hex(32),'max_sessions':4}
    ss={'server':'127.0.0.1','server_port':8388,'password':secret,'method':'aes-256-gcm','mode':'tcp_and_udp','timeout':120}
    lp=g.private('landing-private.json',landing);sp=g.private('ss-private.json',ss)
    g.upload(lp,'landing.json');g.upload(sp,'ss.json')
    echo=g.directory/'echo.py';echo.write_text(ECHO_SCRIPT);g.upload(echo,'echo.py')
    g.upload(DIST/'mptcp-landing','mptcp-landing')
    g.upload(g.executable('x86_64-unknown-linux-musl','ssserver'),'ssserver')
    command=f'''set -eu
cd {g.destination}
chmod 755 mptcp-landing ssserver
chmod 600 landing.json ss.json
./mptcp-landing version
./ssserver --version
sudo systemd-run --unit=mptcp-validation-echo --property=User=tester /usr/bin/python3 {g.destination}/echo.py
sudo systemd-run --unit=mptcp-validation-ss --property=User=tester {g.destination}/ssserver -c {g.destination}/ss.json
printf '[Unit]\nDescription=Pre-existing legacy service isolation fixture\n[Service]\nExecStart=/usr/bin/sleep infinity\n' | sudo tee /etc/systemd/system/mptcp-validation-legacy.service >/dev/null
sudo systemctl daemon-reload
sudo systemctl start mptcp-validation-legacy.service
sudo sha256sum /etc/systemd/system/mptcp-validation-legacy.service > legacy-before.sha256
./mptcp-landing <<'EOF'
0
EOF
sudo ./mptcp-landing install --config {g.destination}/landing.json --yes --start
sleep 2
sudo ./mptcp-landing doctor
sudo ./mptcp-landing status
sudo systemctl show mptcp-userspace-landing.service -p ActiveState -p SubState -p DynamicUser -p MainPID -p User -p NRestarts
sudo systemd-analyze verify /etc/systemd/system/mptcp-userspace-landing.service
sudo sha256sum -c legacy-before.sha256
sudo systemctl is-active mptcp-validation-legacy.service mptcp-validation-ss.service
'''
    out=g.run(command,timeout=150).decode()
    assert landing['transport_key'] not in out and secret not in out
    (REPORT/'debian-install.txt').write_text(out)
    print(out,flush=True)


def lifecycle(g:Guest):
    g.upload(DIST/'mptcp-landing','mptcp-landing')
    g.upload(ROOT/'macos/build/mptcp-landing-upgrade','mptcp-landing-upgrade')
    checksum=hashlib.sha256((ROOT/'macos/build/mptcp-landing-upgrade').read_bytes()).hexdigest()
    original=hashlib.sha256((DIST/'mptcp-landing').read_bytes()).hexdigest()
    command=f'''set -eu
cd {g.destination}
sudo /usr/local/bin/mptcp-landing stop
! sudo systemctl is-active --quiet mptcp-userspace-landing.service
sudo /usr/local/bin/mptcp-landing start
sleep 1
sudo /usr/local/bin/mptcp-landing restart
sleep 1
sudo /usr/local/bin/mptcp-landing upgrade --source {g.destination}/mptcp-landing-upgrade --sha256 {checksum}
sleep 1
printf '{checksum}  /usr/local/bin/mptcp-landing\n' | sudo sha256sum -c -
sudo /usr/local/bin/mptcp-landing rollback
sleep 1
printf '{original}  /usr/local/bin/mptcp-landing\n' | sudo sha256sum -c -
sudo /usr/local/bin/mptcp-landing logs
sudo /usr/local/bin/mptcp-landing uninstall --yes
! test -e /usr/local/bin/mptcp-landing
sudo test -f /etc/mptcp-userspace/config.json
sudo ./mptcp-landing install --config /etc/mptcp-userspace/config.json --yes --start
sleep 1
sudo /usr/local/bin/mptcp-landing status
sudo sha256sum -c legacy-before.sha256
sudo systemctl is-active mptcp-validation-legacy.service mptcp-validation-ss.service
'''
    out=g.run(command,timeout=180).decode()
    key=json.loads((g.directory/'landing-private.json').read_text())['transport_key']
    assert key not in out
    (REPORT/'debian-lifecycle.txt').write_text(out)
    print(out[-6000:],flush=True)


def interactive(g:Guest):
    """Real no-argument menu lifecycle in the disposable Debian guest."""
    g.upload(DIST/'mptcp-landing','mptcp-landing')
    checksum=hashlib.sha256((ROOT/'macos/build/mptcp-landing-upgrade').read_bytes()).hexdigest()
    g.upload(ROOT/'macos/build/mptcp-landing-upgrade','mptcp-landing-upgrade')
    before=g.run('sha256sum '+g.destination+'/ss.json')
    g.run('sudo /usr/local/bin/mptcp-landing uninstall --yes')
    defaults='\n'*7+'yes\n'
    answers=('1\n'+defaults+'3\n7\n2\n'+defaults+'5\n4\n6\n8\n10\n'+
             g.destination+'/mptcp-landing-upgrade\n'+checksum+
             '\n11\nyes\n7\n12\nyes\nno\n0\n')
    out=g.run('sudo '+g.destination+'/mptcp-landing',input=answers.encode(),timeout=180).decode()
    key=json.loads((g.directory/'landing-private.json').read_text())['transport_key']
    assert key not in out and '操作未完成' not in out,out[-3000:]
    for phrase in ['安装完成','配置已保存','升级完成','已回滚','卸载完成']:
        assert phrase in out,phrase
    check=g.run(f'''set -eu
! test -e /usr/local/bin/mptcp-landing
sudo test -f /etc/mptcp-userspace/config.json
cd {g.destination}
sudo sha256sum -c legacy-before.sha256
sudo systemctl is-active mptcp-validation-legacy.service mptcp-validation-ss.service
sudo ./mptcp-landing install --config /etc/mptcp-userspace/config.json --yes --start
sudo systemd-analyze verify /etc/systemd/system/mptcp-userspace-landing.service
''',timeout=90).decode()
    assert g.run('sha256sum '+g.destination+'/ss.json')==before
    (REPORT/'debian-interactive.txt').write_text(out+'\n'+check+'\nPASS: real no-argument menu lifecycle; SS config and legacy fixture unchanged\n')
    print('PASS: Debian no-argument menu install/config/doctor/start/stop/restart/status/logs/upgrade/rollback/uninstall; retained config reinstalled; legacy fixture and SS unchanged',flush=True)


class Relay:
    def __init__(self,target:int):
        self.disabled=threading.Event();self.stop=threading.Event();self.lock=threading.Lock();self.connections=set()
        self.tcp=socket.socket();self.tcp.bind(('127.0.0.1',0));self.port=self.tcp.getsockname()[1];self.tcp.listen(8);self.tcp.settimeout(.2)
        self.udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);self.udp.bind(('127.0.0.1',self.port));self.udp.settimeout(.2)
        self.remote=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);self.remote.connect(('127.0.0.1',target));self.remote.settimeout(.2)
        self.client=None;self.target=target;self.bytes=[0,0];self.threads=[]
        self.spawn(self.accept);self.spawn(self.udp_out);self.spawn(self.udp_in)
    def spawn(self,fn,*args):
        t=threading.Thread(target=fn,args=args,daemon=True);self.threads.append(t);t.start()
    def accept(self):
        while not self.stop.is_set():
            try:a,_=self.tcp.accept()
            except socket.timeout:continue
            except OSError:return
            if self.disabled.is_set():a.close();continue
            self.spawn(self.forward,a)
    def forward(self,a):
        try:b=socket.create_connection(('127.0.0.1',self.target),timeout=5)
        except OSError:a.close();return
        for c in (a,b):c.settimeout(1)
        with self.lock:
            if self.disabled.is_set():a.close();b.close();return
            self.connections.update((a,b))
        def copy(src,dst,direction):
            while not self.stop.is_set() and not self.disabled.is_set():
                try:data=src.recv(32768)
                except socket.timeout:continue
                except OSError:return
                if not data:
                    try:dst.shutdown(socket.SHUT_WR)
                    except OSError:pass
                    return
                try:dst.sendall(data);self.bytes[direction]+=len(data)
                except OSError:return
        worker=threading.Thread(target=copy,args=(a,b,0),daemon=True);worker.start();copy(b,a,1)
        a.close();b.close();worker.join(2)
        with self.lock:self.connections.discard(a);self.connections.discard(b)
    def udp_out(self):
        while not self.stop.is_set():
            try:data,source=self.udp.recvfrom(65536)
            except socket.timeout:continue
            except OSError:return
            self.client=source
            if not self.disabled.is_set():
                try:self.remote.send(data)
                except OSError:pass
    def udp_in(self):
        while not self.stop.is_set():
            try:data=self.remote.recv(65536)
            except socket.timeout:continue
            except OSError:continue
            if self.client and not self.disabled.is_set():
                try:self.udp.sendto(data,self.client)
                except OSError:pass
    def fail(self):
        self.disabled.set()
        with self.lock:
            for c in list(self.connections):
                try:c.shutdown(socket.SHUT_RDWR)
                except OSError:pass
                c.close()
    def recover(self):self.disabled.clear()
    def close(self):
        self.stop.set();self.fail();self.tcp.close();self.udp.close();self.remote.close()
        for t in self.threads:t.join(2)


def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]

def exact(c,n):
    out=b''
    while len(out)<n:
        b=c.recv(n-len(out))
        if not b:raise RuntimeError('premature SOCKS/TCP EOF')
        out+=b
    return out

def socks(localport,cmd=1):
    c=socket.create_connection(('127.0.0.1',localport),timeout=20);c.settimeout(45)
    c.sendall(b'\x05\x01\x00');assert exact(c,2)==b'\x05\x00'
    target=b'\x7f\x00\x00\x01'+struct.pack('!H',29080) if cmd==1 else b'\x00'*6
    c.sendall(bytes([5,cmd,0,1])+target);h=exact(c,4)
    assert h[0]==5 and h[1]==0,h
    if h[3]==1:host=socket.inet_ntoa(exact(c,4))
    elif h[3]==4:host=socket.inet_ntop(socket.AF_INET6,exact(c,16))
    elif h[3]==3:host=exact(c,exact(c,1)[0]).decode()
    else:raise RuntimeError('invalid SOCKS address type')
    bound=struct.unpack('!H',exact(c,2))[0]
    return c,(host,bound)

def tcp_transfer(localport,size):
    c,_=socks(localport);data=os.urandom(size);error=[]
    def send():
        try:
            for i in range(0,len(data),32749):c.sendall(data[i:i+32749])
            c.shutdown(socket.SHUT_WR)
        except Exception as e:error.append(e)
    worker=threading.Thread(target=send);worker.start();digest=hashlib.sha256();received=0
    try:
        while True:
            b=c.recv(65536)
            if not b:break
            digest.update(b);received+=len(b)
    except Exception as exc:
        raise RuntimeError(f'SS TCP size={size}, received={received}, sender_errors={error}: {exc}') from exc
    finally:c.close();worker.join(50)
    if worker.is_alive():raise RuntimeError('SS write worker did not stop')
    if error:raise error[0]
    assert received==size and digest.digest()==hashlib.sha256(data).digest(),(received,size)


def udp_transfer(localport,sizes):
    control,address=socks(localport,3)
    try:
        with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as u:
            u.setsockopt(socket.SOL_SOCKET,socket.SO_SNDBUF,1<<20)
            u.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,1<<20)
            u.settimeout(10);u.connect(address)
            for size in sizes:
                payload=os.urandom(size);u.send(b'\x00\x00\x00\x01\x7f\x00\x00\x01'+struct.pack('!H',29080)+payload)
                received=u.recv(65536);assert received[:4]==b'\x00\x00\x00\x01',received[:4]
                assert received[10:]==payload,('UDP changed',size,len(received)-10)
    finally:control.close()


def pcb_count():
    p=subprocess.run(['sysctl','-n','net.inet.mptcp.pcbcount'],capture_output=True,text=True)
    return int(p.stdout.strip()) if p.returncode==0 else None


def run_test(g:Guest,soak_seconds:int,short_connections:int=40,engine_arch:str='arm64'):
    if engine_arch not in ('arm64','amd64'):raise ValueError('unsupported test engine architecture')
    suffix='' if engine_arch=='arm64' else '-amd64'
    engine_path=pathlib.Path(os.environ.get('MPX_TEST_ENGINE', str(ROOT/'macos/build'/('engine-'+engine_arch))))
    engine_command=(['/usr/bin/arch','-x86_64'] if engine_arch=='amd64' else ['/usr/bin/arch','-arm64'])+[str(engine_path)]
    identity=json.loads(checked(engine_command+['version']))
    if identity.get('version')!=VERSION or identity.get('wire_protocol')!=2:
        raise RuntimeError('Test engine is not the expected release/protocol')
    landing=json.loads((g.directory/'landing-private.json').read_text());ss=json.loads((g.directory/'ss-private.json').read_text())
    relays=[Relay(g.meta['landing_port']),Relay(g.meta['landing_port'])]
    entrance,proxy=port(),port();events=[];ready=threading.Event();processes=[];timers=[];logs=[];before=pcb_count()
    started=time.monotonic();pcb_samples=[]
    def sample(stage):
        stat=next((e for e in reversed(events) if e.get('kind')=='stats'),{})
        usage=subprocess.run(['ps','-o','rss=,cputime=','-p',str(engine.pid)],capture_output=True,text=True).stdout.strip()
        pcb_samples.append({'stage':stage,'elapsed_seconds':time.monotonic()-started,'mptcp_pcb':pcb_count(),'logical_connections':stat.get('connections'),'process_rss_kib_and_cpu_time':usage})
    try:
        profile={'schema_version':3,'mode':'userspace_multipath','listen_port':entrance,'relays':[{'host':'127.0.0.1','port':r.port} for r in relays],'tcp_enabled':True,'udp_enabled':True,'transport_key':landing['transport_key']}
        engine=subprocess.Popen(engine_command+['run'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
        processes.append(engine);engine.stdin.write(json.dumps(profile));engine.stdin.close()
        def collect():
            for line in engine.stdout:
                try:event=json.loads(line)
                except ValueError:continue
                events.append(event)
                if event.get('kind')=='listening':ready.set()
        collector=threading.Thread(target=collect,daemon=True);collector.start()
        if not ready.wait(20):raise RuntimeError('engine not ready: '+json.dumps(events[-5:]))
        local=dict(ss);local.update(server='127.0.0.1',server_port=entrance,local_address='127.0.0.1',local_port=proxy,outbound_udp_allow_fragmentation=True)
        cfg=g.private('ss-local-private.json',local);log=(g.directory/'sslocal.log').open('w');logs.append(log)
        p=subprocess.Popen([str(g.executable('aarch64-apple-darwin','sslocal')),'-c',str(cfg),'--tcp-no-delay','--inbound-send-buffer-size','1048576','--inbound-recv-buffer-size','1048576','--outbound-send-buffer-size','1048576','--outbound-recv-buffer-size','1048576'],stdout=log,stderr=subprocess.STDOUT);processes.append(p)
        for _ in range(100):
            try:
                with socket.create_connection(('127.0.0.1',proxy),timeout=.1):break
            except OSError:
                if p.poll() is not None:raise RuntimeError('sslocal failed to start')
                time.sleep(.05)
        tcp_sizes=[0,1,32767,32768,32769,2<<20]
        for size in tcp_sizes:
            print('TCP payload',size,flush=True);tcp_transfer(proxy,size)
        # An independently verified direct SS baseline drops empty application
        # UDP replies. The unmodified macOS sslocal also rejects 48/60 KB sends
        # before MPX with EMSGSIZE (independent encrypted-sink baseline).
        # Raw MPX zero-length and up to 65507-byte datagrams are separate tests.
        udp_sizes=[1,1128,1129,8192];udp_transfer(proxy,udp_sizes)
        print('SS UDP boundaries passed',flush=True);sample('boundaries')
        with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda _:tcp_transfer(proxy,128<<10),range(12)))
        for index in range(short_connections):
            tcp_transfer(proxy,97)
            if (index+1)%500==0:
                sample('short-'+str(index+1));print('Short SS connections',index+1,flush=True)
        sample('short-connections-complete')
        timers=[threading.Timer(.15,relays[0].fail),threading.Timer(1.5,relays[0].recover)]
        for timer in timers:timer.start()
        tcp_transfer(proxy,8<<20)
        for timer in timers:timer.join()
        relays[0].fail();time.sleep(3.5);udp_transfer(proxy,[1,8192]);relays[0].recover();time.sleep(1.5);udp_transfer(proxy,[1,8192])
        soak_start=time.monotonic();soak_bytes=0
        next_sample=soak_start
        while time.monotonic()-soak_start<soak_seconds:
            tcp_transfer(proxy,1<<20);soak_bytes+=1<<20
            if time.monotonic()>=next_sample:
                sample('soak');next_sample=time.monotonic()+30
        sample('soak-complete')
        time.sleep(1.2)
        stats=next(e for e in reversed(events) if e.get('kind')=='stats')
        udpstats=next(e for e in reversed(events) if e.get('kind')=='udp_stats')
        assert stats['paths']==2 and all(p['sent']>0 and p['received']>0 for p in stats['path_stats'])
        assert all(stats.get(k,0)==0 for k in ['connections','reorder_bytes','pending_bytes','receive_allocated_bytes','receive_credit_bytes','ready_frames']),stats
        # Landing snapshots update every two seconds. Wait for bounded drain
        # instead of treating an asynchronously stale snapshot as a data leak.
        drain_deadline=time.monotonic()+6
        while True:
            landing_status=json.loads(g.run('sudo cat /run/mptcp-userspace-landing/status.json'))
            assert landing_status.get('version')==VERSION and landing_status.get('wire_protocol')==2,landing_status
            assert landing_status.get('source_id')==identity.get('source_id'),'Mac/Landing Source-ID mismatch'
            if all(all(s.get(k,0)==0 for k in ['connections','pending_bytes','buffered_bytes','receive_allocated_bytes','receive_credit_bytes','ready_frames']) for s in landing_status['tcp']):break
            assert time.monotonic()<drain_deadline,landing_status
            time.sleep(.25)
        assert before==pcb_count(), 'Native MPTCP PCB changed during isolated Userspace run'
        assert udpstats.get('connections',0)==0 or soak_seconds<65,udpstats
        fd=subprocess.run(['lsof','-nP','-a','-p',str(engine.pid),'-i','-F','Pn'],capture_output=True,text=True)
        assert fd.returncode==0 and fd.stdout.splitlines().count('PTCP')==3,fd.stdout
        sample('idle-after-traffic')
        report={'version':VERSION,'source_id':identity.get('source_id'),'engine_arch':engine_arch,'engine_sha256':hashlib.sha256(engine_path.read_bytes()).hexdigest(),'scope':'Mac engine -> two transparent TCP/UDP relays -> Debian13 amd64 QEMU systemd Landing -> independently built shadowsocks-rust ssserver -> loopback echo, with independent sslocal/SOCKS5 application','shadowsocks_version':g.tools['shadowsocks_version'],'cipher':ss['method'],'tcp_payload_sizes':tcp_sizes,'udp_payload_sizes':udp_sizes,'parallel_streams':12,'parallel_workers':6,'short_ss_connections':short_connections,'pcb_samples':pcb_samples,'ss_empty_udp_baseline':'Direct independent SS also times out for empty application UDP; raw MPX empty datagrams tested separately','ss_large_udp_baseline':'Unmodified macOS sslocal v1.25.0 rejects 48000/60000-byte application UDP with EMSGSIZE before reaching MPX; independent encrypted UDP sink observed 1/1128/8192-byte payloads. Not a claim of large SS UDP interoperability.','ss_test_socket_options':'1 MiB inbound/outbound buffers, outbound_udp_allow_fragmentation=true, tcp_no_delay; test sslocal only, no system sysctl changes','fault_tcp_bytes':8<<20,'udp_path_failure_and_recovery':True,'soak_seconds':time.monotonic()-soak_start,'soak_bytes':soak_bytes,'elapsed_seconds':time.monotonic()-started,'mptcp_pcb_before':before,'mptcp_pcb_after_traffic':pcb_count(),'pcb_scope':'whole-host counter, not an attribution of activity from other programs','engine_tcp':stats,'engine_udp':udpstats,'landing_after':landing_status,'idle_process_sockets':fd.stdout.splitlines(),'relay_wire_bytes':[r.bytes for r in relays]}
        (REPORT/f'real-ss-integration{suffix}.json').write_text(json.dumps(report,indent=2)+'\n')
        (REPORT/f'real-ss-engine-events{suffix}.jsonl').write_text('\n'.join(json.dumps(e) for e in events)+'\n')
        print(json.dumps({k:v for k,v in report.items() if k not in ['idle_process_sockets','engine_tcp','engine_udp']},indent=2),flush=True)
    finally:
        (REPORT/f'latest-ss-engine-events{suffix}.jsonl').write_text('\n'.join(json.dumps(e) for e in events)+'\n')
        print('Last engine events',json.dumps(events[-4:]),flush=True)
        for timer in timers:timer.cancel()
        for p in reversed(processes):
            if p.poll() is None:
                p.terminate()
                try:p.wait(timeout=15)
                except subprocess.TimeoutExpired:p.kill();p.wait(timeout=5)
        for r in relays:r.close()
        for f in logs:f.close()
        print('All integration client/relay processes stopped; PCB=',pcb_count(),flush=True)
        reclaimed=[]
        for child in processes:
            reclaimed.append({'pid':child.pid,'exit_code':child.returncode,'stopped':child.poll() is not None})
        for number in [entrance,proxy]:
            with socket.socket() as probe:
                probe.settimeout(.3)
                assert probe.connect_ex(('127.0.0.1',number))!=0,'test entrance still open'
        (REPORT/f'ss-process-cleanup{suffix}.json').write_text(json.dumps({'processes':reclaimed,'entrances_closed':True,'pcb_after':pcb_count()},indent=2)+'\n')


def cleanup(g:Guest):
    out=g.run(f'''set -eu
cd {g.destination}
sudo /usr/local/bin/mptcp-landing uninstall --yes --purge
! test -e /usr/local/bin/mptcp-landing
! sudo test -e /etc/mptcp-userspace/config.json
sudo sha256sum -c legacy-before.sha256
sudo systemctl is-active mptcp-validation-legacy.service mptcp-validation-ss.service
sudo systemctl stop mptcp-validation-ss.service mptcp-validation-echo.service mptcp-validation-legacy.service
''').decode()
    (REPORT/'debian-uninstall.txt').write_text(out);print(out)


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=['setup','lifecycle','interactive','test','cleanup']);parser.add_argument('--soak-seconds',type=int,default=60);parser.add_argument('--short-connections',type=int,default=40);parser.add_argument('--engine-arch',choices=['arm64','amd64'],default='arm64');args=parser.parse_args()
    REPORT.mkdir(parents=True,exist_ok=True);g=Guest()
    if args.action=='test':run_test(g,max(0,args.soak_seconds),max(0,args.short_connections),args.engine_arch)
    else:globals()[args.action](g)
