#!/usr/bin/env python3
"""Validate the frozen Keychain Broker across changing parent App cdhash values.

The test uses only MPTCPDesk.KeychainBroker.Validation.v1/probe and removes it.
It never reads or writes production MPTCP Desk credentials.
"""
from __future__ import annotations
import argparse
import base64
import hashlib
import json
import pathlib
import re
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
IDENTITY = 'MPTCP Desk Stable Local Code Signing'
EXPECTED = '5df1fa0f97f976a7cae25733ce1e3e86f6dd77b7d7684dcd11a116a80dc83fc9'


def run(args, *, input=None, check=True):
    p=subprocess.run(args,input=input,capture_output=True)
    if check and p.returncode:
        raise RuntimeError(f'{args[0]} exited {p.returncode}: {p.stderr.decode(errors="replace")[-1200:]}')
    return p


def main():
    ap=argparse.ArgumentParser()
    ap.add_argument('--require-signing',action='store_true')
    args=ap.parse_args()
    identities=run(['security','find-identity','-v','-p','codesigning',str(pathlib.Path.home()/'Library/Keychains/login.keychain-db')],check=False).stdout.decode(errors='replace')
    if IDENTITY not in identities:
        if args.require_signing:
            raise SystemExit('stable local code-signing identity unavailable')
        print('SKIP: stable local code-signing identity unavailable')
        return
    encoded=(ROOT/'macos/keychain-broker/MPTCPKeychainBroker.v1.b64').read_text()
    broker_bytes=base64.b64decode(encoded)
    actual=hashlib.sha256(broker_bytes).hexdigest()
    if actual!=EXPECTED:
        raise SystemExit(f'broker hash mismatch: {actual}')
    with tempfile.TemporaryDirectory(prefix='mptcp-broker-gate.') as td:
        td=pathlib.Path(td)
        broker=td/'broker';broker.write_bytes(broker_bytes);broker.chmod(0o700)
        run(['codesign','--verify','--strict',str(broker)])
        archs=set(run(['lipo','-archs',str(broker)]).stdout.decode().split())
        if archs!={'arm64','x86_64'}:
            raise SystemExit(f'broker is not universal: {archs}')
        direct=run([str(broker)],input=b'{"version":1,"operation":"ping"}',check=False)
        if direct.returncode!=77 or b'unauthorized-parent' not in direct.stdout:
            raise SystemExit('broker accepted an unsigned/untrusted parent')
        template=r'''import Foundation
let generation = GENERATION
let broker = URL(fileURLWithPath: CommandLine.arguments[1])
let operation = CommandLine.arguments[2]
let value = CommandLine.arguments.count > 3 ? CommandLine.arguments[3] : nil
var object: [String: Any] = ["version": 1, "operation": operation, "slot": "validation"]
if let value { object["valueBase64"] = Data(value.utf8).base64EncodedString() }
let payload = try JSONSerialization.data(withJSONObject: object)
let input = Pipe(); let output = Pipe(); let process = Process()
process.executableURL = broker; process.standardInput = input; process.standardOutput = output
try process.run(); input.fileHandleForWriting.write(payload); try input.fileHandleForWriting.close(); process.waitUntilExit()
let data = output.fileHandleForReading.readDataToEndOfFile()
FileHandle.standardOutput.write(data)
if generation < 0 { exit(9) }
exit(process.terminationStatus)
'''
        parents=[]
        for generation in (1,2):
            src=td/f'Parent-{generation}.swift'; exe=td/f'parent-{generation}'
            src.write_text(template.replace('GENERATION',str(generation)))
            run(['xcrun','swiftc','-O',str(src),'-o',str(exe)])
            run(['codesign','--force','--identifier','org.mptcp.desktop','--sign',IDENTITY,str(exe)])
            parents.append(exe)
        hashes=[]
        for parent in parents:
            dump=run(['codesign','-dvvv',str(parent)],check=False).stderr.decode(errors='replace')
            m=re.search(r'^CDHash=(\w+)$',dump,re.M)
            if not m: raise SystemExit('missing parent CDHash')
            hashes.append(m.group(1))
        if hashes[0]==hashes[1]: raise SystemExit('test parents unexpectedly have same CDHash')
        def invoke(parent,op,value=None):
            command=[str(parent),str(broker),op]
            if value is not None: command.append(value)
            p=run(command)
            if b'"ok":true' not in p.stdout: raise SystemExit('broker request failed')
            return p.stdout
        try:
            invoke(parents[0],'set','frozen-broker-gate')
            first=json.loads(invoke(parents[0],'get'))
            second=json.loads(invoke(parents[1],'get'))
            expected='ZnJvemVuLWJyb2tlci1nYXRl'
            if first.get('valueBase64')!=expected or second.get('valueBase64')!=expected:
                raise SystemExit('broker value changed across parent generations')
            dump=run(['security','dump-keychain','-a',str(pathlib.Path.home()/'Library/Keychains/login.keychain-db')]).stdout.decode(errors='replace')
            broker_dump=run(['codesign','-dvvv',str(broker)],check=False).stderr.decode(errors='replace')
            m=re.search(r'^CDHash=(\w+)$',broker_dump,re.M)
            if not m or f'description: cdhash:{m.group(1)}' not in dump:
                raise SystemExit('validation item partition is not bound to frozen broker CDHash')
        finally:
            try: invoke(parents[-1],'delete')
            except Exception: pass
        print(f'OK broker_sha256={actual} broker_parent_cdhashes={hashes[0]},{hashes[1]}')

if __name__=='__main__': main()
