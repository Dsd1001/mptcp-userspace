#!/usr/bin/env python3
"""Resume only the pre-provisioned disposable Debian validation guest.
Requires local macos/build/debian-vm.json and verified image/tool preparation.
Never discovers a VPS, changes host networking, or uses production credentials.
"""
from __future__ import annotations
import argparse
import json
import os
import pathlib
import shutil
import signal
import socket
import subprocess
import time

from live_ss import Guest, REPORT, ROOT


def process_command(pid: int) -> str:
    p = subprocess.run(['ps','-p',str(pid),'-o','command='],capture_output=True,text=True)
    return p.stdout.strip() if p.returncode == 0 else ''


def owned(g: Guest, pid: int) -> bool:
    command = process_command(pid)
    return 'qemu-system-x86_64' in command and str(g.directory/'overlay.qcow2') in command


def free_port() -> int:
    with socket.socket() as s:
        s.bind(('127.0.0.1',0))
        return s.getsockname()[1]


def start(g: Guest) -> None:
    if not g.directory.name.startswith('mptcp-debian13-') or not g.directory.is_dir():
        raise RuntimeError('Not a recognized disposable validation directory')
    pid = int(g.meta.get('qemu_pid',0))
    if pid and owned(g,pid):
        print('Disposable guest already running; not spawning a duplicate')
        return
    for name in ['overlay.qcow2','seed.iso','ssh-key','known_hosts']:
        if not (g.directory/name).is_file():
            raise RuntimeError('Missing pre-provisioned guest file: '+name)
    qemu = shutil.which('qemu-system-x86_64')
    if not qemu:
        raise RuntimeError('Install QEMU explicitly before using this opt-in harness')
    # Only loopback host forwards; no listening interface or route modification.
    g.meta['echo_port'] = free_port()
    for number in [g.meta['ssh_port'],g.meta['landing_port'],g.meta['echo_port']]:
        with socket.socket() as s:
            s.bind(('127.0.0.1',number))
    pidfile = g.directory/'validation-070.pid'
    pidfile.unlink(missing_ok=True)
    network = (f"user,id=net0,hostfwd=tcp:127.0.0.1:{g.meta['ssh_port']}-:22,"
               f"hostfwd=tcp:127.0.0.1:{g.meta['landing_port']}-:24001,"
               f"hostfwd=udp:127.0.0.1:{g.meta['landing_port']}-:24001,"
               f"hostfwd=tcp:127.0.0.1:{g.meta['echo_port']}-:29081")
    command = [qemu,'-machine','q35,accel=tcg','-cpu','max','-smp','4','-m','1536',
               '-drive',f'file={g.directory / "overlay.qcow2"},if=virtio,format=qcow2',
               '-drive',f'file={g.directory / "seed.iso"},media=cdrom,format=raw',
               '-netdev',network,'-device','virtio-net-pci,netdev=net0',
               '-display','none','-serial','file:'+str(g.directory/'console-070.log'),
               '-daemonize','-pidfile',str(pidfile)]
    subprocess.run(command,check=True,capture_output=True,timeout=20)
    g.meta['qemu_pid'] = int(pidfile.read_text())
    (ROOT/'macos/build/debian-vm.json').write_text(json.dumps(g.meta,indent=2)+'\n')
    deadline = time.monotonic()+150
    while time.monotonic()<deadline:
        if not owned(g,g.meta['qemu_pid']):
            raise RuntimeError('Disposable guest exited during boot; inspect console-070.log')
        try:
            info = g.run('uname -m; cat /etc/os-release; systemctl --version',timeout=6).decode()
            if 'x86_64' not in info or 'VERSION_ID="13"' not in info:
                raise RuntimeError('Guest does not match Debian 13 amd64')
            REPORT.mkdir(parents=True,exist_ok=True)
            (REPORT/'debian-environment.txt').write_text(info)
            print(info)
            return
        except (subprocess.TimeoutExpired,RuntimeError):
            time.sleep(2)
    raise RuntimeError('Disposable Debian SSH did not become ready')


def stop(g: Guest) -> None:
    pid = int(g.meta.get('qemu_pid',0))
    if not owned(g,pid):
        if process_command(pid):
            raise RuntimeError('PID now belongs to another command; not stopping it')
        print('Disposable guest already stopped')
        return
    try:
        g.run('sudo poweroff',timeout=8)
    except (RuntimeError,subprocess.TimeoutExpired):
        pass
    deadline = time.monotonic()+25
    while owned(g,pid) and time.monotonic()<deadline:
        time.sleep(.5)
    if owned(g,pid):
        os.kill(pid,signal.SIGTERM)
        deadline = time.monotonic()+10
        while owned(g,pid) and time.monotonic()<deadline:
            time.sleep(.2)
    if owned(g,pid):
        raise RuntimeError('Disposable QEMU did not stop')
    REPORT.mkdir(parents=True,exist_ok=True)
    (REPORT/'vm-cleanup.json').write_text(json.dumps({'owned_qemu_stopped':True,'pid':pid,'scope':'Only the pre-provisioned disposable validation VM'},indent=2)+'\n')
    print('Verified: disposable QEMU stopped')


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('action',choices=['start','stop'])
    args=parser.parse_args()
    globals()[args.action](Guest())
