#!/usr/bin/env python3
"""Verify actual DMG and reproduce code from the exact source archive.
No desktop automation, installation or host network changes.
"""
from __future__ import annotations
import importlib.util
import json
import os
import pathlib
import plistlib
import shutil
import struct
import subprocess
import tarfile
import tempfile
import xml.etree.ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('source_manifest', ROOT/'scripts/source-manifest.py')
assert spec and spec.loader
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)


def run(args: list[str], *, cwd=None, env=None, input=None, timeout=180) -> bytes:
    p = subprocess.run(args,cwd=cwd,env=env,input=input,capture_output=True,timeout=timeout)
    if p.returncode:
        raise RuntimeError(f'{pathlib.Path(args[0]).name} exited {p.returncode}: '+p.stderr.decode(errors='replace')[-2000:])
    return p.stdout


def macho_sections(path: pathlib.Path) -> dict[str, dict]:
    """Compare executable/data sections, excluding signing/link-edit metadata.
    Code signing and linker UUIDs need not be byte-identical to prove section
    equality; this deliberately does NOT claim a reproducible DMG container.
    """
    data = path.read_bytes()
    if len(data)<32 or struct.unpack_from('<I',data)[0]!=0xfeedfacf:
        raise ValueError('Expected thin little-endian Mach-O 64')
    count = struct.unpack_from('<I',data,16)[0]
    offset = 32
    sections = {}
    for _ in range(count):
        cmd,size = struct.unpack_from('<II',data,offset)
        if size<8 or offset+size>len(data):
            raise ValueError('Malformed Mach-O command')
        if cmd==0x19:
            nsects = struct.unpack_from('<I',data,offset+64)[0]
            for i in range(nsects):
                at=offset+72+i*80
                if at+80>offset+size:
                    raise ValueError('Malformed section table')
                name=data[at:at+16].split(b'\0')[0].decode()
                segment=data[at+16:at+32].split(b'\0')[0].decode()
                address,length = struct.unpack_from('<QQ',data,at+32)
                position=struct.unpack_from('<I',data,at+48)[0]
                flags=struct.unpack_from('<I',data,at+64)[0]
                zero=(flags&255) in (1,12,18)
                if not zero and position+length>len(data):
                    raise ValueError('Section extends past file')
                sections[segment+','+name]={'size':length,'address':address,'flags':flags,
                    'sha256':None if zero else source.sha(data[position:position+length])}
        offset+=size
    if '__TEXT,__text' not in sections:
        raise ValueError('Executable code section missing')
    return sections


def main() -> None:
    version=(ROOT/'macos/VERSION').read_text().strip()
    out=ROOT/'dist'/('userspace-'+version)
    files=source.collect();sums=source.manifest(files);identity=source.sha(sums)
    if (out/'SOURCE_SHA256SUMS').read_bytes()!=sums or (out/'SOURCE_ID').read_text().strip()!=identity:
        raise ValueError('Source freeze is stale')
    source_name=f'MPTCP-Userspace-{version}-source.tar.gz'
    dmg_name=f'MPTCP-Desk-{version}-universal.dmg'
    go_path=ROOT/'macos/build/go-path'
    go=os.environ.get('MPTCP_GO') or (go_path.read_text().strip() if go_path.exists() else shutil.which('go'))
    if not go:
        raise ValueError('MPTCP_GO is required')
    flags=f'-s -w -buildid= -X mptcp-desktop/engine/multipath.SourceID={identity}'
    env=dict(os.environ,GOTOOLCHAIN='local',GOCACHE='/tmp/mptcp-desktop-cache',GOMODCACHE='/tmp/mptcp-desktop-mod')
    checks=[];reproduced={}
    with tempfile.TemporaryDirectory(prefix='mpx-source-verification-',dir='/tmp') as temporary:
        work=pathlib.Path(temporary);frozen=work/'source';frozen.mkdir()
        expected=dict(files,SOURCE_SHA256SUMS=sums,SOURCE_ID=(identity+'\n').encode())
        sparkle_archive=ROOT/'macos/build/vendor/Sparkle-2.10.0.tar.xz'
        if not sparkle_archive.is_file() or source.sha(sparkle_archive.read_bytes())!='c2bf58aa8387266ac179357b1415d6f2635f044da8be41042af32425dae6da0c':
            raise ValueError('Pinned Sparkle 2.10.0 archive missing or checksum mismatch')
        sparkle=work/'sparkle';sparkle.mkdir()
        run(['tar','-xJf',str(sparkle_archive),'-C',str(sparkle),'./Sparkle.framework','./bin/sign_update'])
        sign_update=sparkle/'bin/sign_update'
        appcast=out/'appcast.xml'
        if not appcast.is_file():
            raise ValueError('Signed appcast.xml missing')
        feed=ET.parse(appcast).getroot()
        ns={'sparkle':'http://www.andymatuschak.org/xml-namespaces/sparkle'}
        item=feed.find('./channel/item')
        if item is None:
            raise ValueError('appcast item missing')
        short=item.findtext('sparkle:shortVersionString',namespaces=ns)
        build=item.findtext('sparkle:version',namespaces=ns)
        enclosure=item.find('enclosure')
        if short!=version or build!=str(plistlib.loads((ROOT/'macos/Info.plist').read_bytes())['CFBundleVersion']) or enclosure is None:
            raise ValueError('appcast version/build mismatch')
        signature=enclosure.attrib.get('{http://www.andymatuschak.org/xml-namespaces/sparkle}edSignature','')
        if not signature or int(enclosure.attrib.get('length','0'))!=(out/dmg_name).stat().st_size:
            raise ValueError('appcast DMG signature/length metadata invalid')
        expected_url=f'https://github.com/Dsd1001/mptcp-userspace/releases/download/v{version}/{dmg_name}'
        if enclosure.attrib.get('url')!=expected_url:
            raise ValueError('appcast DMG URL mismatch')
        run([str(sign_update),'--verify',str(out/dmg_name),signature])
        run([str(sign_update),'--verify',str(appcast)])
        checks.append('Sparkle appcast and DMG EdDSA signatures verified')
        with tarfile.open(out/source_name,'r:gz') as archive:
            if set(archive.getnames())!=set(expected):
                raise ValueError('Frozen source archive inventory differs')
            for entry in archive:
                stream=archive.extractfile(entry) if entry.isfile() else None
                if entry.pax_headers or stream is None:
                    raise ValueError('Non-regular source archive entry')
                data=stream.read()
                if data!=expected[entry.name]:
                    raise ValueError('Frozen source archive bytes differ')
                path=frozen/entry.name;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(data)
        if source.manifest(source.collect(frozen))!=sums:
            raise ValueError('Extracted frozen tree differs')
        linux_artifacts = [
            ('mptcp-client-linux-amd64', 'client', 'amd64'),
            ('mptcp-client-linux-arm64', 'client', 'arm64'),
            ('mptcp-landing', 'landing', 'amd64'),
            ('mptcp-landing-linux-arm64', 'landing', 'arm64'),
        ]
        for artifact, component, arch in linux_artifacts:
            rebuilt=work/artifact
            package='.' if component=='client' else './cmd/mptcp-landing'
            run([go,'build','-trimpath','-buildvcs=false','-ldflags='+flags,'-o',str(rebuilt),package],
                cwd=frozen/'macos/engine',env=dict(env,CGO_ENABLED='0',GOOS='linux',GOARCH=arch))
            if rebuilt.read_bytes()!=(out/artifact).read_bytes():
                raise ValueError(f'{component} {arch} is not byte-reproducible from frozen source')
            checks.append(f'Linux {arch} {component} ELF byte-identical rebuild from source archive')
            reproduced[f'{component}-linux-{arch}']={'sha256':source.sha(rebuilt.read_bytes()),'comparison':f'entire {component} binary'}

        provision_flags=f'-s -w -buildid= -X main.Version={version} -X main.SourceID={identity}'
        for artifact, arch in [('mpx-provision','amd64'),('mpx-provision-linux-arm64','arm64')]:
            rebuilt=work/artifact
            run([go,'build','-trimpath','-buildvcs=false','-ldflags='+provision_flags,'-o',str(rebuilt),'.'],
                cwd=frozen/'provisioning',env=dict(env,CGO_ENABLED='0',GOOS='linux',GOARCH=arch))
            if rebuilt.read_bytes()!=(out/artifact).read_bytes():
                raise ValueError(f'Provisioning {arch} is not byte-reproducible from frozen source')
            checks.append(f'Linux {arch} Provisioning ELF byte-identical rebuild from source archive')
            reproduced[f'provision-linux-{arch}']={'sha256':source.sha(rebuilt.read_bytes()),'comparison':'entire Provisioning binary'}

        buildinfos=['MPTCP-Desk.BUILDINFO']
        for artifact, _, _ in linux_artifacts:
            buildinfos.append(artifact+'.BUILDINFO')
        buildinfos += ['mpx-provision.BUILDINFO','mpx-provision-linux-arm64.BUILDINFO']
        for info in buildinfos:
            if 'Source-ID: '+identity not in (out/info).read_text():
                raise ValueError('Buildinfo does not bind source: '+info)
        run(['hdiutil','verify',str(out/dmg_name)])
        mounted=plistlib.loads(run(['hdiutil','attach','-readonly','-nobrowse','-plist',str(out/dmg_name)]))
        mount=next(pathlib.Path(e['mount-point']) for e in mounted['system-entities'] if 'mount-point' in e)
        try:
            app=mount/'MPTCP Desk.app'
            run(['codesign','--verify','--deep','--strict',str(app)])
            info=plistlib.loads((app/'Contents/Info.plist').read_bytes())
            if info['CFBundleShortVersionString']!=version or info.get('MPTCPSourceID')!=identity or info.get('LSUIElement') is not True:
                raise ValueError('App version/source/menu-bar metadata differs')
            if info.get('SUPublicEDKey')!='2ADwJkrQ2XxjFo4bC3mjQkxjGBpsj3hfP7JTV0wdtek=':
                raise ValueError('Sparkle public update key differs')
            if info.get('SUFeedURL')!='https://github.com/Dsd1001/mptcp-userspace/releases/latest/download/appcast.xml':
                raise ValueError('Sparkle feed URL differs')
            packaged_sparkle=app/'Contents/Frameworks/Sparkle.framework'
            if not packaged_sparkle.is_dir():
                raise ValueError('Sparkle.framework missing from app')
            run(['codesign','--verify','--deep','--strict',str(packaged_sparkle)])
            resources=app/'Contents/Resources'
            for p in [resources/'SOURCE_ID',mount/'SOURCE_ID']:
                if p.read_text().strip()!=identity:
                    raise ValueError('Packaged source ID differs')
            engine=resources/'mptcp-desktop-engine';ui=app/'Contents/MacOS/MPTCPDesk'
            for binary in [engine,ui]:
                if set(run(['lipo','-archs',str(binary)]).decode().split())!={'arm64','x86_64'}:
                    raise ValueError('Universal architectures missing')
            sdk=run(['xcrun','--sdk','macosx','--show-sdk-path']).decode().strip()
            for arch,goarch in [('arm64','arm64'),('x86_64','amd64')]:
                event=json.loads(run(['/usr/bin/arch','-'+arch,str(engine),'version'],timeout=30))
                if event.get('source_id')!=identity or event.get('version')!=version or event.get('wire_protocol')!=4 or event.get('capability_revision')!=4:
                    raise ValueError('Actual packaged engine identity differs')
                rebuilt=work/('engine-'+goarch)
                cc=f'clang -arch {arch} -isysroot {sdk} -mmacosx-version-min=13.0'
                run([go,'build','-trimpath','-buildvcs=false','-ldflags='+flags,'-o',str(rebuilt),'.'],
                    cwd=frozen/'macos/engine',env=dict(env,CGO_ENABLED='1',GOOS='darwin',GOARCH=goarch,CC=cc))
                thin=work/('packaged-engine-'+goarch)
                run(['lipo',str(engine),'-thin',arch,'-output',str(thin)])
                if macho_sections(thin)!=macho_sections(rebuilt):
                    raise ValueError('Packaged engine sections differ from frozen-source rebuild: '+arch)
                rebuilt_ui=work/('MPTCPDesk-'+goarch)
                run(['xcrun','swiftc','-O','-swift-version','5','-parse-as-library','-target',arch+'-apple-macosx13.0',
                     '-module-cache-path','/tmp/mptcp-swift-cache','-debug-prefix-map',str(frozen)+'=.',
                     '-F',str(sparkle),'-framework','Sparkle','-Xlinker','-rpath','-Xlinker','@executable_path/../Frameworks',
                     str(frozen/'macos/Lifecycle.swift'),str(frozen/'macos/Profile.swift'),str(frozen/'macos/RemoteControl.swift'),
                     str(frozen/'macos/UpdateController.swift'),str(frozen/'macos/App.swift'),'-o',str(rebuilt_ui)])
                thin_ui=work/('packaged-ui-'+goarch)
                run(['lipo',str(ui),'-thin',arch,'-output',str(thin_ui)])
                if macho_sections(thin_ui)!=macho_sections(rebuilt_ui):
                    raise ValueError('Packaged App sections differ from frozen-source rebuild: '+arch)
                reproduced['darwin-'+goarch]={'engine_sections':macho_sections(thin),'app_sections':macho_sections(thin_ui),
                    'comparison':'all Mach-O sections including executable code and constant data; excludes signatures/link-edit/UUID'}
            for name in ['README.zh-CN.md','VALIDATION.md','tcp-profile.example.json','userspace-profile.example.json']:
                if (resources/name).read_bytes()!=files['macos/'+name]:
                    raise ValueError('Packaged documentation/example differs: '+name)
            for name in ['DEPLOYMENT.zh-CN.md','PROTOCOL.md','PROVISIONING.md','VALIDATION.md','ADAPTIVE-FLOW-CONTROL.md','MPX3-CREDIT.md','SCHEDULER-MODES.md','REV2-SHARED-CREDIT.md']:
                if (resources/'docs/userspace'/name).read_bytes()!=files['docs/userspace/'+name]:
                    raise ValueError('Packaged protocol/deployment documentation differs')
            run([str(engine),'validate'],input=files['macos/tcp-profile.example.json'])
            invalid=subprocess.run([str(engine),'validate'],input=files['macos/userspace-profile.example.json'],capture_output=True)
            if invalid.returncode==0:
                raise ValueError('Placeholder transport key accepted')
            profile=json.loads(files['macos/userspace-profile.example.json']);profile['transport_key']='0a'*32
            run([str(engine),'validate'],input=json.dumps(profile).encode())
            checks.extend(['read-only DMG and strict ad-hoc signature','embedded Sparkle framework and pinned public update key',
                'ARM and x86_64 packaged engine execution','App and engine section-identical rebuilds from frozen source','schema2/3 compatibility and placeholder rejection',
                'packaged documentation equals frozen source'])
        finally:
            run(['hdiutil','detach',str(mount)])
    if source.manifest(source.collect())!=sums:
        raise ValueError('Sources changed during verification')
    artifacts=[dmg_name,'appcast.xml',source_name,'MPTCP-Desk.BUILDINFO',
        'mptcp-client-linux-amd64','mptcp-client-linux-amd64.BUILDINFO',
        'mptcp-client-linux-arm64','mptcp-client-linux-arm64.BUILDINFO',
        'mptcp-landing','mptcp-landing.BUILDINFO',
        'mptcp-landing-linux-arm64','mptcp-landing-linux-arm64.BUILDINFO',
        'mpx-provision','mpx-provision.BUILDINFO',
        'mpx-provision-linux-arm64','mpx-provision-linux-arm64.BUILDINFO']
    receipt={'version':version,'source_id':identity,'verified':True,'checks':checks,'reproduced':reproduced,
             'artifact_sha256':{name:source.sha((out/name).read_bytes()) for name in artifacts},
             'limitations':['Not a reproducible DMG filesystem container','No Developer ID notarization',
                            'Intel execution is Rosetta, not physical Intel hardware','No GUI/keychain authorization interaction']}
    (out/'PROVENANCE.json').write_text(json.dumps(receipt,indent=2)+'\n')
    print(json.dumps({k:v for k,v in receipt.items() if k!='reproduced'},indent=2))


if __name__=='__main__':
    main()
